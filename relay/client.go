package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/neoromantic/ai-usage/internal/snapshot"
	"github.com/neoromantic/ai-usage/internal/team"
)

// Client publishes this device and reads the team back.
type Client struct {
	BaseURL string
	Key     *team.Key
	HTTP    *http.Client
	Now     func() time.Time
}

// Device is one verified team document.
type Device struct {
	Body []byte
	Doc  snapshot.Doc
	// Since is when the relay first stored the device, or zero when it
	// does not say.
	Since time.Time
}

var errNoRelay = errors.New("relay is not configured")

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 20 * time.Second}
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Client) url(path string) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if base == "" {
		return "", errNoRelay
	}
	return base + path, nil
}

// Publish stores this device's snapshot. body must be the exact JSON to sign.
func (c *Client) Publish(ctx context.Context, device string, body []byte) error {
	req, err := c.request(ctx, http.MethodPut, device, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(HeaderKey, encode(c.Key.Public()))
	req.Header.Set(HeaderSig, encode(c.Key.Sign(SnapshotMessage(body))))
	_, err = c.do(req)
	var status *ErrStatus
	if errors.As(err, &status) && status.Code == http.StatusUnprocessableEntity {
		return rejected{status}
	}
	return err
}

// ErrRejected is what Publish fails with when the relay refuses the snapshot
// itself (HTTP 422), as a relay older than the collector refuses fields it
// does not know. The error is also the relay's *ErrStatus.
var ErrRejected = errors.New("relay rejected the snapshot")

type rejected struct{ *ErrStatus }

func (r rejected) Unwrap() []error { return []error{ErrRejected, r.ErrStatus} }

// PullResult is a team read: the documents that verify and decode, one per
// device, and how many were left out.
type PullResult struct {
	Devices []Device
	// Unverified counts documents that do not verify with the team key or
	// do not name the device and team the relay filed them under, and extra
	// documents for a device the relay already listed.
	Unverified int
	// Undecodable counts documents signed with the team key that this
	// release cannot read, such as one from a newer release that added
	// fields to the snapshot.
	Undecodable int
}

// Pull reads the team and returns documents that verify and decode, one per
// device, sorted by device id.
func (c *Client) Pull(ctx context.Context) (PullResult, error) {
	var out PullResult
	req, err := c.signed(ctx, http.MethodGet, "")
	if err != nil {
		return out, err
	}
	raw, err := c.do(req)
	if err != nil {
		return out, err
	}
	var res ListResponse
	if err := json.Unmarshal(raw, &res); err != nil {
		return out, fmt.Errorf("relay: team read is not JSON")
	}
	at := map[string]int{}
	for _, d := range res.Devices {
		body, err1 := decode(d.Body)
		sig, err2 := decode(d.Sig)
		if err1 != nil || err2 != nil || !team.Verify(c.Key.Public(), SnapshotMessage(body), sig) {
			out.Unverified++
			continue
		}
		doc, err := snapshot.Decode(body)
		if err != nil {
			out.Undecodable++
			continue
		}
		if doc.Team != c.Key.Fingerprint() || doc.Device != d.Device {
			out.Unverified++
			continue
		}
		// A relay that lists a device twice, even with genuine older snapshots,
		// would have its tokens added twice. Keep the newest.
		if i, ok := at[doc.Device]; ok {
			out.Unverified++
			if doc.CollectedAt.After(out.Devices[i].Doc.CollectedAt) {
				out.Devices[i] = Device{Body: body, Doc: doc, Since: d.Since}
			}
			continue
		}
		at[doc.Device] = len(out.Devices)
		out.Devices = append(out.Devices, Device{Body: body, Doc: doc, Since: d.Since})
	}
	sort.Slice(out.Devices, func(i, j int) bool { return out.Devices[i].Doc.Device < out.Devices[j].Doc.Device })
	return out, nil
}

// Prices reads the relay's price list, which needs no key. A relay that
// serves none fails with an *ErrStatus of 404.
func (c *Client) Prices(ctx context.Context) ([]byte, error) {
	u, err := c.url("/v1/prices")
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	return c.do(req)
}

// Remove deletes one device's document.
func (c *Client) Remove(ctx context.Context, device string) error {
	req, err := c.signed(ctx, http.MethodDelete, device)
	if err != nil {
		return err
	}
	_, err = c.do(req)
	return err
}

func (c *Client) signed(ctx context.Context, method, device string) (*http.Request, error) {
	req, err := c.request(ctx, method, device, nil)
	if err != nil {
		return nil, err
	}
	at := c.now()
	req.Header.Set(HeaderKey, encode(c.Key.Public()))
	req.Header.Set(HeaderTime, strconv.FormatInt(at.Unix(), 10))
	req.Header.Set(HeaderSig, encode(c.Key.Sign(RequestMessage(method, c.Key.Fingerprint(), device, at))))
	return req, nil
}

// request builds a request for the team, or for one device of it when device
// is set.
func (c *Client) request(ctx context.Context, method, device string, body []byte) (*http.Request, error) {
	path := "/v1/teams/" + c.Key.Fingerprint()
	if device != "" {
		path += "/devices/" + device
	}
	u, err := c.url(path)
	if err != nil {
		return nil, err
	}
	return http.NewRequestWithContext(ctx, method, u, bytes.NewReader(body))
}

func (c *Client) do(req *http.Request) ([]byte, error) {
	resp, err := c.http().Do(req)
	if err != nil {
		return nil, fmt.Errorf("relay unreachable: %w", err)
	}
	defer resp.Body.Close()
	// A full team read at DefaultLimits is about 4.4 MB. A self-hosted relay
	// with higher limits may send more; past 8 MiB the read fails as not JSON.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("relay: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		return nil, &ErrStatus{Code: resp.StatusCode, Msg: e.Error}
	}
	return raw, nil
}
