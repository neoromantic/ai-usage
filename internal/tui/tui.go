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
	// Report is the report shown first.
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
	// Detail draws what a row of the page opens, false when the report no
	// longer has it; nil is view.Detail.
	Detail func(view.Report, view.Options, view.Pick) (view.Page, bool)
	// Cost runs `ai-usage cost` with args, such as set ACCOUNT --price 20,
	// and says what it did in a line; nil leaves out `e` and the report's
	// questions.
	Cost func(args []string) (string, error)
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
	pollEvery, spinEvery time.Duration

	report view.Report
	opts   view.Options
	page   view.Page
	theme  view.Theme

	width, height int
	// top is the first line of the page shown, helpTop of the help.
	top, helpTop int
	help         bool

	// picking says a row of the page is picked, and picked is it. open is
	// the row whose detail shows in the page's place, nil while the page
	// shows, and pageTop is where the page was scrolled to when it opened.
	picking bool
	picked  view.Pick
	open    *view.Pick
	pageTop int

	// busy is a collection `r` started; gen tells its spinner from an
	// earlier one's.
	busy       bool
	gen, frame int

	loading  bool
	loadedAt time.Time
	// sig is the watched files as the page last loaded them.
	sig string

	refreshErr, loadErr string
	// note is what the last cost command did.
	note string

	// prompt asks a question of the report's, or edits a subscription,
	// over the key bar; nil when nothing is asked. dismissed are the
	// questions answered or put away while the view is open.
	prompt    *prompt
	dismissed map[string]bool
}

// New is the view before it knows the terminal's size.
func New(c Config) Model {
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Render == nil {
		c.Render = view.Render
	}
	if c.Detail == nil {
		c.Detail = view.Detail
	}
	m := Model{
		cfg:       c,
		jobs:      &jobs{ctx: context.Background()},
		pollEvery: pollEvery,
		spinEvery: spinEvery,
		report:    c.Report,
		opts:      c.Options,
		loadedAt:  c.Now(),
		sig:       signature(c.Watch),
	}
	m.opts.MatrixScroll = 0
	m.theme = view.NewTheme(m.opts.Dark)
	return m
}

func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{tea.RequestBackgroundColor}
	if m.cfg.Load != nil {
		cmds = append(cmds, m.poll())
	}
	return tea.Batch(cmds...)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		bottom := m.top > 0 && m.top >= m.maxTop()
		m.width, m.height = msg.Width, msg.Height
		m.draw()
		if bottom && !m.choosing() {
			m.top = m.maxTop()
		}
		m.nextQuestion()
	case tea.BackgroundColorMsg:
		m.opts.Dark = msg.IsDark()
		m.theme = view.NewTheme(m.opts.Dark)
		m.draw()
	case tea.KeyPressMsg:
		return m.key(msg.String())
	case tea.MouseWheelMsg:
		m.wheel(msg.Mouse())
	case tea.MouseClickMsg:
		m.click(msg.Mouse())
	case pollMsg:
		cmds := []tea.Cmd{m.poll()}
		if !m.loading && (signature(m.cfg.Watch) != m.sig || m.cfg.Now().Sub(m.loadedAt) >= reloadEvery) {
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
		m.nextQuestion()
	case spinMsg:
		if !m.busy || msg.gen != m.gen {
			break
		}
		m.frame++
		m.draw()
		return m, m.spin()
	case costDoneMsg:
		m.note, m.refreshErr = msg.text, ""
		if msg.err != nil {
			m.note, m.refreshErr = "", firstLine(msg.err)
		}
		m.draw()
		return m, m.load()
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
	if k == "ctrl+c" {
		return m, tea.Quit
	}
	if m.prompt != nil && !m.help {
		if mm, cmd, ok := m.promptKey(k); ok {
			return mm, cmd
		}
	}
	switch k {
	case "q":
		return m, tea.Quit
	case "esc", "backspace":
		// Back: from the help, a detail, or a pick. Esc on the page quits.
		switch {
		case m.help:
			m.help = false
		case m.open != nil:
			m.close()
		case m.picking:
			m.picking = false
		case k == "esc":
			return m, tea.Quit
		}
		return m, nil
	case "?":
		m.help, m.helpTop = !m.help, 0
		return m, nil
	}
	if m.choosing() {
		// Picking, ↑ and ↓ move the pick, which the page follows.
		switch k {
		case "up", "k":
			m.move(m.pickIndex() - 1)
			return m, nil
		case "down", "j":
			m.move(m.pickIndex() + 1)
			return m, nil
		case "g", "home":
			m.move(0)
			return m, nil
		case "G", "end":
			m.move(len(m.page.Picks) - 1)
			return m, nil
		case "enter":
			m.openPick()
			return m, nil
		}
	}
	switch k {
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
	m.keepPick()
	switch k {
	case "enter":
		m.startPick()
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
		// share; the mode waits for the matrix. From dollars it goes to
		// share.
		if m.shareKey() {
			m.opts.Share = !m.opts.Share || m.opts.Cost
			m.opts.Cost = false
			m.draw()
		}
	case "$":
		if m.costKey() {
			m.opts.Cost = !m.opts.Cost
			m.draw()
		}
	case "e":
		m.edit()
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
		m.scrollMatrix(-1)
	case e.Button == tea.MouseWheelRight, e.Button == tea.MouseWheelDown && shift:
		m.scrollMatrix(1)
	case e.Button == tea.MouseWheelUp:
		m.scroll(-wheelLines)
	case e.Button == tea.MouseWheelDown:
		m.scroll(wheelLines)
	}
	m.keepPick()
}

// click picks the row clicked on the page, and opens it when it is picked.
func (m *Model) click(e tea.Mouse) {
	if e.Button != tea.MouseLeft || m.help || m.open != nil || e.Y < 1 || e.Y > m.bodyHeight() {
		return
	}
	i := e.Y - 1
	if i < m.pinned(m.top) {
		return
	}
	line := m.top + i
	for _, k := range m.page.Picks {
		if k.Line != line {
			continue
		}
		if m.picking && m.picked.Line == line {
			m.openPick()
			return
		}
		m.picking, m.picked = true, k
		return
	}
}

// choosing says ↑ and ↓ move the pick: a row is picked on the page.
func (m Model) choosing() bool { return m.picking && m.open == nil && !m.help }

// startPick picks the first row in sight, else the nearest one.
func (m *Model) startPick() {
	if m.open != nil || len(m.page.Picks) == 0 {
		return
	}
	from, to := m.inSight()
	m.picking, m.picked = true, nearest(m.page.Picks, from, to)
	m.show(m.picked.Line)
}

// pickIndex is the pick's place among the page's picks.
func (m Model) pickIndex() int {
	for i, k := range m.page.Picks {
		if k.Line == m.picked.Line {
			return i
		}
	}
	return 0
}

// move picks the page's i-th pick, within the first and the last, and
// scrolls the page to it.
func (m *Model) move(i int) {
	if len(m.page.Picks) == 0 {
		return
	}
	m.picked = m.page.Picks[clamp(i, len(m.page.Picks)-1)]
	m.show(m.picked.Line)
}

// keepPick picks a row in sight once the pick has scrolled out of it.
func (m *Model) keepPick() {
	if !m.choosing() {
		return
	}
	if from, to := m.inSight(); m.picked.Line < from || m.picked.Line >= to {
		if k := nearest(m.page.Picks, from, to); k.Line >= from && k.Line < to {
			m.picked = k
		}
	}
}

// openPick shows the picked row's detail in the page's place, from its top.
func (m *Model) openPick() {
	k := m.picked
	m.open, m.pageTop, m.top = &k, m.top, 0
	m.draw()
}

// close goes back from a detail to the page, where it was, with its row
// still picked and in sight.
func (m *Model) close() {
	m.open, m.top = nil, m.pageTop
	if m.draw(); m.picking {
		m.show(m.picked.Line)
	}
}

// repick finds the picked row on a page drawn again: the same account or
// device nearest to where it was, else the row nearest to it. A page with
// no rows to pick has no pick.
func (m *Model) repick() {
	if !m.picking {
		return
	}
	ks := m.page.Picks
	if len(ks) == 0 {
		m.picking = false
		return
	}
	var same []view.Pick
	for _, k := range ks {
		if k.Same(m.picked) {
			same = append(same, k)
		}
	}
	if len(same) > 0 {
		ks = same
	}
	m.picked = nearest(ks, m.picked.Line, m.picked.Line+1)
}

// nearest is the first of ks on the lines from from to to, else the one
// nearest to them.
func nearest(ks []view.Pick, from, to int) view.Pick {
	best, dist := ks[0], 1<<30
	for _, k := range ks {
		d := 0
		switch {
		case k.Line < from:
			d = from - k.Line
		case k.Line >= to:
			d = k.Line - to + 1
		}
		if d < dist {
			best, dist = k, d
		}
	}
	return best
}

// inSight are the page's lines on screen under the head of DEVICES, from
// the first to one past the last.
func (m Model) inSight() (from, to int) {
	return m.top + m.pinned(m.top), m.top + m.bodyHeight()
}

// show scrolls the page the least that puts line in sight.
func (m *Model) show(line int) {
	if from, to := m.inSight(); line >= from && line < to {
		return
	}
	top := m.top
	if line >= m.top+m.bodyHeight() {
		top = line - m.bodyHeight() + 1
	}
	top = min(top, line)
	// Over the rows of DEVICES, the head pinned at the top covers a few.
	for top > 0 && line < top+m.pinned(top) {
		top--
	}
	m.top = clamp(top, m.maxTop())
}

// scroll moves the page, or the help when it is open, by n lines.
func (m *Model) scroll(n int) {
	if m.help {
		m.helpTop = clamp(m.helpTop+n, m.maxHelpTop())
		return
	}
	m.top = clamp(m.top+n, m.maxTop())
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
	if m.help || m.open != nil || m.statusView() || s < 0 || n > 0 && !m.matrixRight() {
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
	return m.open == nil && !m.statusView() && (m.opts.MatrixScroll > 0 || m.matrixRight())
}

// statusView says DEVICES shows its status view, not the matrix.
func (m Model) statusView() bool { return m.opts.DeviceStatus && m.page.DeviceViews }

// shareKey says % does something: the matrix shows, and has a column to
// share or is in share mode.
func (m Model) shareKey() bool {
	return m.open == nil && !m.statusView() && (m.page.MatrixColumns > 0 || m.opts.Share || m.opts.Cost)
}

// costKey says $ does something: the matrix shows, has a column, and the
// report has a cost, or the matrix is in dollars.
func (m Model) costKey() bool {
	return m.open == nil && !m.statusView() && (m.page.MatrixColumns > 0 && m.report.Cost != nil || m.opts.Cost)
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
	load, now, sig := m.cfg.Load, m.loadedAt, signature(m.cfg.Watch)
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
	// The page follows the pick only when it was in sight, or comes back
	// from a detail that is gone: a reload must not scroll back to a pick
	// scrolled out of sight.
	from, to := m.inSight()
	follow := m.open != nil || from <= m.picked.Line && m.picked.Line < to
	if m.open != nil {
		if p, ok := m.cfg.Detail(m.report, m.options(m.opts), *m.open); ok {
			m.page = p
			m.top = clamp(m.top, m.maxTop())
			m.helpTop = clamp(m.helpTop, m.maxHelpTop())
			return
		}
		// What it showed is gone from the report: back to the page.
		m.open, m.top = nil, m.pageTop
	}
	m.page = m.render(m.opts)
	// Scrolled right on a terminal that grew, or a matrix that lost columns:
	// scroll left while the columns on the right still all show. The status
	// view keeps the matrix's scroll for when it shows again.
	for m.opts.MatrixScroll > 0 && !m.statusView() {
		o := m.opts
		o.MatrixScroll--
		p := m.render(o)
		if o.MatrixScroll+p.MatrixShown < p.MatrixColumns {
			break
		}
		m.opts, m.page = o, p
	}
	m.top = clamp(m.top, m.maxTop())
	m.helpTop = clamp(m.helpTop, m.maxHelpTop())
	if m.repick(); m.picking && follow {
		// Rows the period reorders take the pick along, and the page
		// follows it.
		m.show(m.picked.Line)
	}
}

func (m Model) render(o view.Options) view.Page { return m.cfg.Render(m.report, m.options(o)) }

// options are o as the view draws with them: for its width, interactive,
// with the spinner while a collection runs.
func (m Model) options(o view.Options) view.Options {
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
	return o
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
	if m.prompt != nil {
		h--
	}
	return max(0, h)
}

func (m Model) maxTop() int { return max(0, len(m.page.Body)-m.bodyHeight()) }

func (m Model) maxHelpTop() int { return max(0, len(m.helpLines())-m.bodyHeight()) }

func (m Model) statusText() string {
	if m.refreshErr != "" {
		return m.refreshErr
	}
	if m.loadErr != "" {
		return m.loadErr
	}
	return m.note
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
		case m.choosing() && top+i == m.picked.Line:
			rows = append(rows, fit(m.highlight(lines[top+i]), w))
		case top+i < len(lines):
			rows = append(rows, fit(lines[top+i], w))
		default:
			rows = append(rows, "")
		}
	}
	if m.prompt != nil {
		rows = append(rows, m.promptLine())
	}
	if s := m.statusText(); s != "" {
		k := styleError
		if s == m.note {
			k = styleAction
		}
		rows = append(rows, fit(m.style(" "+s, k), w))
	}
	rule := "─"
	if m.opts.ASCII {
		rule = "-"
	}
	rows = append(rows, m.style(strings.Repeat(rule, w), styleFaint), bar)
	return strings.Join(rows, "\n")
}

// highlight is the picked row: its text, as wide as the page, in reverse
// video, in the accent color with color.
func (m Model) highlight(line string) string {
	s := ansi.Strip(line)
	return m.style(s+strings.Repeat(" ", max(0, m.page.Width-ansi.StringWidth(s))), stylePick)
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

func clamp(v, hi int) int { return max(0, min(v, hi)) }

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
