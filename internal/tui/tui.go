// Package tui is the interactive report: the page the static report prints,
// on the alternate screen, with its header at the top, a key bar at the
// bottom, and the page scrolling between them.
package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"

	"github.com/neoromantic/ai-usage/internal/view"
)

// Config is what the view shows and how it gets a newer report.
type Config struct {
	// Report is the report shown first. A zero one is loaded at the start.
	Report view.Report
	// Options say how the page is drawn. A Width above 0 fixes the layout
	// width; 0 follows the terminal. Period, Share, and DeviceStatus are
	// where the view starts. The view sets Interactive, MatrixScroll, and
	// Busy itself, and Dark once the terminal says what its background is.
	Options view.Options
	// Profile is the escapes the terminal is written with, as the static
	// report would write them; Unknown lets Bubble Tea detect them.
	Profile colorprofile.Profile
	// Load rebuilds the report from disk as of now, without collecting.
	Load func(now time.Time) (view.Report, error)
	// Refresh collects now; nil leaves `r` out.
	Refresh func(ctx context.Context) error
	// Stopping is called after the view closes while a collection it
	// started still runs, before Run waits for it to stop.
	Stopping func()
	// Watch are the files a run saves; a change to one reloads the report.
	Watch []string
	// Now is the clock; nil is time.Now.
	Now func() time.Time
	// Render draws the page; nil is view.Render.
	Render func(view.Report, view.Options) view.Page
}

const (
	// pollEvery is how often the view looks for a run that saved new state.
	pollEvery = 2 * time.Second
	// reloadEvery redraws relative times, such as "collected 7m ago".
	reloadEvery = 30 * time.Second
	// spinEvery is the spinner's frame time while a collection runs.
	spinEvery = 100 * time.Millisecond
	// wheelLines is how far one notch of the mouse wheel scrolls.
	wheelLines = 3
)

var (
	spinUTF8  = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	spinASCII = []string{"|", "/", "-", `\`}
)

type (
	// pollMsg looks at the watched files, and at how old the page is.
	pollMsg time.Time
	// spinMsg turns the spinner of one collection.
	spinMsg struct{ gen int }
	// loadedMsg is a report Load built, from the watched files as they
	// were when it started.
	loadedMsg struct {
		report view.Report
		err    error
		sig    string
	}
	// refreshedMsg ends a collection `r` started.
	refreshedMsg struct{ err error }
)

// Run shows the view on the terminal of in and out until the person quits.
// A collection still running then stops, and Run returns once it has.
func Run(ctx context.Context, c Config, in io.Reader, out io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	m := New(c)
	m.jobs = &jobs{ctx: ctx}
	opts := []tea.ProgramOption{tea.WithContext(ctx), tea.WithInput(in), tea.WithOutput(out)}
	if c.Profile != colorprofile.Unknown {
		opts = append(opts, tea.WithColorProfile(c.Profile))
	}
	// Bubble Tea restores the terminal on its way out, after a panic too.
	_, err := tea.NewProgram(m, opts...).Run()
	cancel()
	if m.jobs.stop() && c.Stopping != nil {
		c.Stopping()
	}
	m.jobs.wg.Wait()
	if errors.Is(err, tea.ErrInterrupted) || errors.Is(err, context.Canceled) {
		// Stopped by a signal, as Ctrl-C stops the static report.
		return nil
	}
	return err
}

// Model is the view's state. Its methods never touch the terminal, so tests
// drive it with messages.
type Model struct {
	cfg  Config
	jobs *jobs
	// Intervals, which tests stretch so that no timer fires in them.
	pollEvery, reloadEvery, spinEvery time.Duration

	report view.Report
	opts   view.Options
	page   view.Page
	theme  view.Theme

	width, height int
	// top is the first line of the page shown, helpTop of the help.
	top, helpTop int
	help         bool

	// busy is a collection `r` started; gen tells its spinner from an
	// earlier one's.
	busy       bool
	gen, frame int

	loading  bool
	loadedAt time.Time
	// sig is the watched files as the page last loaded them.
	sig string

	refreshErr, loadErr string
}

// New is the view before it knows the terminal's size.
func New(c Config) Model {
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Render == nil {
		c.Render = view.Render
	}
	m := Model{
		cfg:         c,
		jobs:        &jobs{ctx: context.Background()},
		pollEvery:   pollEvery,
		reloadEvery: reloadEvery,
		spinEvery:   spinEvery,
		report:      c.Report,
		opts:        c.Options,
		loadedAt:    c.Now(),
		sig:         signature(c.Watch),
	}
	m.opts.Interactive = true
	m.opts.MatrixScroll = 0
	m.opts.Busy = ""
	m.theme = view.NewTheme(m.opts.Dark)
	return m
}

func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{tea.RequestBackgroundColor}
	if m.cfg.Load != nil {
		cmds = append(cmds, m.poll())
		if m.report.GeneratedAt.IsZero() {
			cmds = append(cmds, loadCmd(m.cfg.Load, m.cfg.Now(), m.sig))
		}
	}
	return tea.Batch(cmds...)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		bottom := m.top > 0 && m.top >= m.maxTop()
		m.width, m.height = msg.Width, msg.Height
		m.draw()
		if bottom {
			m.top = m.maxTop()
		}
	case tea.BackgroundColorMsg:
		m.opts.Dark = msg.IsDark()
		m.theme = view.NewTheme(m.opts.Dark)
		m.draw()
	case tea.KeyPressMsg:
		return m.key(msg.String())
	case tea.MouseWheelMsg:
		m.wheel(msg.Mouse())
	case pollMsg:
		cmds := []tea.Cmd{m.poll()}
		if !m.loading && (signature(m.cfg.Watch) != m.sig || m.cfg.Now().Sub(m.loadedAt) >= m.reloadEvery) {
			cmds = append(cmds, m.load())
		}
		return m, tea.Batch(cmds...)
	case loadedMsg:
		m.loading, m.sig = false, msg.sig
		if msg.err != nil {
			m.loadErr = "reload failed: " + firstLine(msg.err)
			m.draw()
			break
		}
		m.loadErr = ""
		m.report = msg.report
		m.draw()
	case spinMsg:
		if !m.busy || msg.gen != m.gen {
			break
		}
		m.frame++
		m.draw()
		return m, m.spin()
	case refreshedMsg:
		m.busy = false
		if msg.err != nil {
			m.refreshErr = "refresh failed: " + firstLine(msg.err)
		}
		m.draw()
		// Show what the collection saved, or what it waited for.
		cmd := m.load()
		return m, cmd
	}
	return m, nil
}

func (m Model) key(k string) (tea.Model, tea.Cmd) {
	switch k {
	case "ctrl+c", "q":
		return m, tea.Quit
	case "esc":
		if !m.help {
			return m, tea.Quit
		}
		m.help = false
		return m, nil
	case "?":
		m.help, m.helpTop = !m.help, 0
		return m, nil
	case "up", "k":
		m.scroll(-1)
	case "down", "j":
		m.scroll(1)
	case "pgup":
		m.scrollScreen(-1)
	case "pgdown", "space":
		m.scrollScreen(1)
	case "g", "home":
		m.scroll(-1 << 30)
	case "G", "end":
		m.scroll(1 << 30)
	}
	if m.help {
		return m, nil
	}
	switch k {
	case "left", "h":
		m.scrollMatrix(-1)
	case "right", "l":
		m.scrollMatrix(1)
	case "p":
		m.setPeriod(m.opts.Period.Next())
	case "1":
		m.setPeriod(view.Today)
	case "7":
		m.setPeriod(view.Week)
	case "3":
		m.setPeriod(view.Month)
	case "9":
		m.setPeriod(view.Quarter)
	case "%":
		// Share mode can always be left, even when the matrix is gone,
		// as after a refresh that left one device. The status view has no
		// share; the mode waits for the matrix.
		if m.shareKey() {
			m.opts.Share = !m.opts.Share
			m.draw()
		}
	case "s":
		// The two views of DEVICES are as tall, so the page keeps its top.
		if m.page.DeviceViews {
			m.opts.DeviceStatus = !m.opts.DeviceStatus
			m.draw()
		}
	case "r":
		return m.refresh()
	}
	return m, nil
}

func (m *Model) wheel(e tea.Mouse) {
	shift := e.Mod.Contains(tea.ModShift)
	switch {
	case e.Button == tea.MouseWheelLeft, e.Button == tea.MouseWheelUp && shift:
		if !m.help {
			m.scrollMatrix(-1)
		}
	case e.Button == tea.MouseWheelRight, e.Button == tea.MouseWheelDown && shift:
		if !m.help {
			m.scrollMatrix(1)
		}
	case e.Button == tea.MouseWheelUp:
		m.scroll(-wheelLines)
	case e.Button == tea.MouseWheelDown:
		m.scroll(wheelLines)
	}
}

// scroll moves the page, or the help when it is open, by n lines.
func (m *Model) scroll(n int) {
	if m.help {
		m.helpTop = clamp(m.helpTop+n, 0, max(0, len(m.helpLines())-m.bodyHeight()))
		return
	}
	m.top = clamp(m.top+n, 0, m.maxTop())
}

// scrollScreen moves the page, or the help when it is open, a screen up or
// down. The head of DEVICES pinned over its rows hides no line from it: a
// screen down starts under the head with the line under the last one shown,
// and a screen up ends with the line over the first one shown.
func (m *Model) scrollScreen(dir int) {
	n := m.bodyHeight()
	switch {
	case m.help:
		m.scroll(dir * n)
	case dir < 0:
		m.scroll(m.pinned(m.top) - n)
	default:
		// The furthest top whose first line in sight, under the head where
		// it is pinned, is no further down than the line under the last one.
		next := m.top + n
		top := min(next, m.maxTop())
		for top > m.top+1 && top+m.pinned(top) > next {
			top--
		}
		m.scroll(top - m.top)
	}
}

// pinned is how many lines of the head of DEVICES, its title, the group
// headings, and the column headers, the page keeps at its top when it is
// scrolled to line top: all of them from the line the title scrolls off on,
// while TOTAL is still under them, as a table keeps a sticky header; else
// none. They cover the head's own lines and the rows scrolled past, each of
// which is in sight a line up. A body no taller than the head keeps none.
func (m Model) pinned(top int) int {
	h := m.page.DevicesHead
	n := h[1] - h[0]
	if n <= 0 || m.bodyHeight() <= n || top <= h[0] || top+n >= m.page.DevicesEnd {
		return 0
	}
	return n
}

// scrollMatrix moves the matrix by n subscription columns, no further left
// than its first and no further right than showing its last.
func (m *Model) scrollMatrix(n int) {
	s := m.opts.MatrixScroll + n
	if m.statusView() || s < 0 || n > 0 && !m.matrixRight() {
		return
	}
	m.opts.MatrixScroll = s
	m.draw()
}

// matrixRight says the matrix has columns off the right edge.
func (m Model) matrixRight() bool {
	return m.opts.MatrixScroll+m.page.MatrixShown < m.page.MatrixColumns
}

// matrixCut says the matrix shows and does not fit, so ← and → do
// something.
func (m Model) matrixCut() bool {
	return !m.statusView() && (m.opts.MatrixScroll > 0 || m.matrixRight())
}

// statusView says DEVICES shows its status view, not the matrix.
func (m Model) statusView() bool { return m.opts.DeviceStatus && m.page.DeviceViews }

// shareKey says % does something: the matrix shows, and has a column to
// share or is in share mode.
func (m Model) shareKey() bool {
	return !m.statusView() && (m.page.MatrixColumns > 0 || m.opts.Share)
}

func (m *Model) setPeriod(p view.Period) {
	if m.opts.Period != p {
		m.opts.Period = p
		m.draw()
	}
}

func (m Model) refresh() (tea.Model, tea.Cmd) {
	if m.cfg.Refresh == nil || m.busy {
		return m, nil
	}
	m.busy, m.refreshErr = true, ""
	m.gen++
	m.frame = 0
	m.draw()
	j, f := m.jobs, m.cfg.Refresh
	run := func() tea.Msg {
		if !j.start() {
			return nil
		}
		defer j.done()
		return refreshedMsg{err: f(j.ctx)}
	}
	return m, tea.Batch(run, m.spin())
}

func (m Model) spin() tea.Cmd {
	gen := m.gen
	return tea.Tick(m.spinEvery, func(time.Time) tea.Msg { return spinMsg{gen} })
}

func (m Model) poll() tea.Cmd {
	return tea.Tick(m.pollEvery, func(t time.Time) tea.Msg { return pollMsg(t) })
}

// load rebuilds the report in the background, as of now.
func (m *Model) load() tea.Cmd {
	if m.cfg.Load == nil {
		return nil
	}
	m.loading = true
	m.loadedAt = m.cfg.Now()
	return loadCmd(m.cfg.Load, m.loadedAt, signature(m.cfg.Watch))
}

func loadCmd(load func(time.Time) (view.Report, error), now time.Time, sig string) tea.Cmd {
	return func() tea.Msg {
		r, err := load(now)
		return loadedMsg{report: r, err: err, sig: sig}
	}
}

// draw renders the page again for the terminal's width and the view's
// options, and keeps the matrix and the page scrolled within their bounds.
func (m *Model) draw() {
	if m.width <= 0 {
		return
	}
	m.page = m.render(m.opts)
	// The status view keeps the matrix's scroll for when it shows again.
	if m.opts.MatrixScroll > 0 && m.page.MatrixColumns == 0 && !m.statusView() {
		m.opts.MatrixScroll = 0
		m.page = m.render(m.opts)
	}
	// Scrolled right on a terminal that grew, or a matrix that lost columns:
	// scroll left while the columns on the right still all show.
	for m.opts.MatrixScroll > 0 && !m.statusView() {
		o := m.opts
		o.MatrixScroll--
		p := m.render(o)
		if o.MatrixScroll+p.MatrixShown < p.MatrixColumns {
			break
		}
		m.opts, m.page = o, p
	}
	m.top = clamp(m.top, 0, m.maxTop())
	m.helpTop = clamp(m.helpTop, 0, max(0, len(m.helpLines())-m.bodyHeight()))
}

func (m Model) render(o view.Options) view.Page {
	o.Width = m.layoutWidth()
	o.Interactive = true
	o.Busy = ""
	if m.busy {
		frames := spinUTF8
		if o.ASCII {
			frames = spinASCII
		}
		o.Busy = frames[m.frame%len(frames)]
	}
	return m.cfg.Render(m.report, o)
}

func (m Model) layoutWidth() int {
	if m.cfg.Options.Width > 0 {
		return m.cfg.Options.Width
	}
	return m.width
}

// bodyHeight is the rows between the header and the rule above the key bar.
func (m Model) bodyHeight() int {
	h := m.height - 3
	if m.statusText() != "" {
		h--
	}
	return max(0, h)
}

func (m Model) maxTop() int { return max(0, len(m.page.Body)-m.bodyHeight()) }

func (m Model) statusText() string {
	if m.refreshErr != "" {
		return m.refreshErr
	}
	return m.loadErr
}

func (m Model) View() tea.View {
	v := tea.NewView(m.screen())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

// screen is the header, the page or the help, a status line when something
// failed, the rule, and the key bar, one row each, height rows in all. The
// page keeps the head of DEVICES at its top while the rows scroll under it.
func (m Model) screen() string {
	w, h := m.width, m.height
	if w <= 0 || h <= 0 {
		return ""
	}
	bar := m.keyBar()
	switch h {
	case 1:
		return bar
	case 2:
		return fit(m.page.Header, w) + "\n" + bar
	}
	rows := make([]string, 0, h)
	rows = append(rows, fit(m.page.Header, w))
	lines, top, pin := m.page.Body, m.top, m.pinned(m.top)
	if m.help {
		lines, top, pin = m.helpLines(), m.helpTop, 0
	}
	n := m.bodyHeight()
	for i := range n {
		switch {
		case i < pin:
			// The head of DEVICES, as the page draws it, scrolled sideways
			// with the matrix.
			rows = append(rows, fit(lines[m.page.DevicesHead[0]+i], w))
		case top+i < len(lines):
			rows = append(rows, fit(lines[top+i], w))
		default:
			rows = append(rows, "")
		}
	}
	if s := m.statusText(); s != "" {
		rows = append(rows, fit(m.style(" "+s, styleError), w))
	}
	rule := "─"
	if m.opts.ASCII {
		rule = "-"
	}
	rows = append(rows, m.style(strings.Repeat(rule, w), styleFaint), bar)
	return strings.Join(rows, "\n")
}

// fit cuts a line to the terminal's width; the page is never narrower than
// 80 columns.
func fit(s string, w int) string {
	if ansi.StringWidth(s) <= w {
		return s
	}
	return ansi.Truncate(s, w, "")
}

func firstLine(err error) string {
	s, _, _ := strings.Cut(err.Error(), "\n")
	return s
}

func clamp(v, lo, hi int) int { return max(lo, min(v, hi)) }

// signature is the watched files' sizes and change times; a run that saves
// state replaces the file, which changes both.
func signature(paths []string) string {
	var b strings.Builder
	for _, p := range paths {
		if fi, err := os.Stat(p); err == nil {
			fmt.Fprintf(&b, "%d:%d;", fi.ModTime().UnixNano(), fi.Size())
		} else {
			b.WriteString("-;")
		}
	}
	return b.String()
}

// jobs are the collections the view started. On its way out the view
// cancels them and waits: a collection holds the run lock and saves state.
type jobs struct {
	ctx     context.Context
	mu      sync.Mutex
	closed  bool
	running int
	wg      sync.WaitGroup
}

// start counts a collection in, unless the view is on its way out.
func (j *jobs) start() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return false
	}
	j.running++
	j.wg.Add(1)
	return true
}

func (j *jobs) done() {
	j.mu.Lock()
	j.running--
	j.mu.Unlock()
	j.wg.Done()
}

// stop lets no collection start, and says whether one still runs; wg then
// waits for it.
func (j *jobs) stop() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.closed = true
	return j.running > 0
}
