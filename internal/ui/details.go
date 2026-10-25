package ui

import (
	"context"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/widget"
	"github.com/libp2p/go-libp2p/core/peer"

	"twocan/internal/core"
	"twocan/internal/diagtext"
	"twocan/internal/errtext"
	"twocan/internal/p2p"
)

const (
	// detailsRefresh is how often an open details dialog reloads.
	detailsRefresh = 2 * time.Second
	// detailsWidth and detailsHeight size the details dialog: addresses
	// are long, and the check adds a line per step.
	detailsWidth  = 680
	detailsHeight = 600
)

// detailsView is the connection details dialog for one contact: what the
// node knows about the connection, refreshed while open, and a step-by-step
// connection check. Its methods run on the Fyne UI goroutine.
type detailsView struct {
	api  core.API
	id   peer.ID
	name string
	ctx  context.Context

	// async runs slow API calls off the UI goroutine and onUI brings their
	// results back to it; tests make both synchronous.
	async, onUI func(func())

	fields  *fyne.Container // label/value pairs
	check   *widget.Button
	steps   *fyne.Container // one label per check step
	verdict *widget.Label
	copy    *widget.Button
	report  string // the finished check, for Copy

	content fyne.CanvasObject
}

// showDetails opens the details dialog for contact id, named name. It
// pings the contact once, refreshes until closed, and cancels a running
// check when closed.
func showDetails(api core.API, w fyne.Window, id peer.ID, name string) {
	ctx, cancel := context.WithCancel(context.Background())
	dv := newDetailsView(ctx, api, id, name, func(f func()) { go f() }, fyne.Do)

	d := dialog.NewCustom("Connection to "+name, "Close", dv.content, w)
	d.SetOnClosed(cancel)
	d.Resize(fyne.NewSize(detailsWidth, detailsHeight))
	d.Show()

	dv.async(func() {
		if _, err := api.Ping(ctx, id); err == nil {
			dv.onUI(dv.refresh)
		}
	})
	go func() {
		t := time.NewTicker(detailsRefresh)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				fyne.Do(func() {
					if ctx.Err() == nil {
						dv.refresh()
					}
				})
			}
		}
	}()
}

// newDetailsView builds the dialog's content and loads the details once.
func newDetailsView(ctx context.Context, api core.API, id peer.ID, name string, async, onUI func(func())) *detailsView {
	dv := &detailsView{api: api, id: id, name: name, ctx: ctx, async: async, onUI: onUI}
	dv.fields = container.New(layout.NewFormLayout())
	dv.check = widget.NewButton("Check connection", dv.runCheck)
	dv.check.Importance = widget.HighImportance
	dv.steps = container.NewVBox()
	dv.verdict = widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	dv.verdict.Wrapping = fyne.TextWrapWord
	dv.copy = widget.NewButton("Copy results", func() { fyne.CurrentApp().Clipboard().SetContent(dv.report) })
	dv.copy.Disable()

	intro := widget.NewLabel("The check tries every way of reaching them, one step at a time, and says what went wrong.")
	intro.Wrapping = fyne.TextWrapWord
	dv.content = container.NewVScroll(container.NewVBox(
		dv.fields,
		widget.NewSeparator(),
		intro,
		container.NewHBox(dv.check, dv.copy),
		dv.steps,
		dv.verdict,
	))
	dv.refresh()
	return dv
}

// refresh reloads the details.
func (dv *detailsView) refresh() {
	d, err := dv.api.ContactDetails(dv.ctx, dv.id)
	if err != nil {
		dv.setFields([]diagtext.Field{{Label: "Error", Value: errtext.FriendlyError(err)}})
		return
	}
	dv.setFields(diagtext.Details(d, time.Now()))
}

func (dv *detailsView) setFields(fs []diagtext.Field) {
	objs := make([]fyne.CanvasObject, 0, 2*len(fs))
	for _, f := range fs {
		label := widget.NewLabelWithStyle(f.Label, fyne.TextAlignTrailing, fyne.TextStyle{Bold: true})
		value := widget.NewLabel(f.Value)
		value.Wrapping = fyne.TextWrapBreak // addresses have no spaces to wrap at
		value.Selectable = true
		objs = append(objs, label, value)
	}
	dv.fields.Objects = objs
	dv.fields.Refresh()
}

// runCheck starts a connection check, showing each step as it goes.
func (dv *detailsView) runCheck() {
	dv.check.Disable()
	dv.copy.Disable()
	dv.steps.RemoveAll()
	dv.verdict.SetText("Checking… this can take a minute.")
	dv.async(func() {
		res, err := dv.api.CheckConnection(dv.ctx, dv.id, func(i int, s p2p.Step) {
			dv.onUI(func() { dv.setStep(i, s) })
		})
		dv.onUI(func() { dv.finish(res, err) })
	})
}

// setStep shows step i, adding rows up to it.
func (dv *detailsView) setStep(i int, s p2p.Step) {
	for len(dv.steps.Objects) <= i {
		l := widget.NewLabel("")
		l.Wrapping = fyne.TextWrapWord
		dv.steps.Add(l)
	}
	l := dv.steps.Objects[i].(*widget.Label)
	l.Importance = stepImportance(s.Status)
	l.SetText(diagtext.StepLine(s))
}

func stepImportance(s p2p.StepStatus) widget.Importance {
	switch s {
	case p2p.StepOK:
		return widget.SuccessImportance
	case p2p.StepWarn:
		return widget.WarningImportance
	case p2p.StepFail:
		return widget.DangerImportance
	case p2p.StepSkipped, p2p.StepRunning:
		return widget.LowImportance
	}
	return widget.MediumImportance
}

// finish shows the check's verdict and enables Copy.
func (dv *detailsView) finish(res p2p.CheckResult, err error) {
	dv.check.Enable()
	if err != nil {
		dv.verdict.SetText(errtext.FriendlyError(err))
		return
	}
	dv.verdict.SetText(res.Verdict)
	dv.report = diagtext.Report(dv.name, time.Now(), res)
	dv.copy.Enable()
	dv.refresh()
}
