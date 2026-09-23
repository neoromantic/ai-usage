package relay

import (
	"bytes"
	"context"
	"encoding/json"
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
}

// ErrNoRelay is returned when no relay URL is configured.
var ErrNoRelay = errNoRelay

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
		return "", ErrNoRelay
	}
	return base + path, nil
}

// Publish stores this device's snapshot. body must be the exact JSON to sign.
func (c *Client) Publish(ctx context.Context, device string, body []byte) error {
	path := "/v1/teams/" + c.Key.Fingerprint() + "/devices/" + device
	u, err := c.url(path)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(HeaderKey, encode(c.Key.Public()))
	req.Header.Set(HeaderSig, encode(c.Key.Sign(SnapshotMessage(body))))
	_, err = c.do(req)
	return err
}

// Pull reads the team and returns documents that verify and decode, one per
// device. Documents that do not verify, and extra documents for a device the
// relay already listed, are counted in bad and left out.
func (c *Client) Pull(ctx context.Context) (devices []Device, bad int, err error) {
	req, err := c.signed(ctx, http.MethodGet, "")
	if err != nil {
		return nil, 0, err
	}
	raw, err := c.do(req)
	if err != nil {
		return nil, 0, err
	}
	var res ListResponse
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, 0, fmt.Errorf("relay: team read is not JSON")
	}
	at := map[string]int{}
	for _, d := range res.Devices {
		body, err1 := decode(d.Body)
		sig, err2 := decode(d.Sig)
		if err1 != nil || err2 != nil || !team.Verify(c.Key.Public(), SnapshotMessage(body), sig) {
			bad++
			continue
		}
		doc, err := snapshot.Decode(body)
		if err != nil || doc.Team != c.Key.Fingerprint() || doc.Device != d.Device {
			bad++
			continue
		}
		// A relay that lists a device twice, even with genuine older snapshots,
		// would have its tokens added twice. Keep the newest.
		if i, ok := at[doc.Device]; ok {
			bad++
			if doc.CollectedAt.After(devices[i].Doc.CollectedAt) {
				devices[i] = Device{Body: body, Doc: doc}
			}
			continue
		}
		at[doc.Device] = len(devices)
		devices = append(devices, Device{Body: body, Doc: doc})
	}
	sort.Slice(devices, func(i, j int) bool { return devices[i].Doc.Device < devices[j].Doc.Device })
	return devices, bad, nil
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
	fp := c.Key.Fingerprint()
	path := "/v1/teams/" + fp
	if device != "" {
		path += "/devices/" + device
	}
	u, err := c.url(path)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return nil, err
	}
	at := c.now()
	req.Header.Set(HeaderKey, encode(c.Key.Public()))
	req.Header.Set(HeaderTime, strconv.FormatInt(at.Unix(), 10))
	req.Header.Set(HeaderSig, encode(c.Key.Sign(RequestMessage(method, fp, device, at))))
	return req, nil
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
