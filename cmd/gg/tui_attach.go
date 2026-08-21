package main

import (
	"context"
	"io"

	"github.com/VedranJanjetovic/gg/internal/cli"
	"github.com/VedranJanjetovic/gg/internal/state"
	"github.com/VedranJanjetovic/gg/internal/tui"
)

type tuiRunFunc func(context.Context, state.ProjectState, tui.Loader, tui.Actions, io.Reader, io.Writer, ...tui.Option) error

type projectTUIAttacher struct {
	input  io.Reader
	output io.Writer
	run    tuiRunFunc
}

func (a projectTUIAttacher) Attach(ctx context.Context, attachment cli.ProjectAttachment) error {
	options := []tui.Option{tui.WithPendingPipeline(tui.DefaultPendingPipeline())}
	if attachment.Notice != "" {
		options = append(options, tui.WithInitialNotice(attachment.Notice))
	}
	if attachment.GroomingPending {
		options = append(options, tui.WithGroomingPending(true))
	}
	return a.run(
		ctx,
		attachment.Project,
		tui.Loader(attachment.Load),
		tui.Actions{Start: attachment.Start, Stop: attachment.Stop, Resume: attachment.Resume, OpenCode: attachment.OpenCode, OpenTerminal: attachment.OpenTerminal},
		a.input,
		a.output,
		options...,
	)
}
