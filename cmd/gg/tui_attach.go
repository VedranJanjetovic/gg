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
	input       io.Reader
	output      io.Writer
	run         tuiRunFunc
	updateCheck tui.UpdateChecker
}

func (a projectTUIAttacher) Attach(ctx context.Context, attachment cli.ProjectAttachment) error {
	options := []tui.Option{tui.WithPendingPipeline(tui.DefaultPendingPipeline())}
	if a.updateCheck != nil {
		options = append(options, tui.WithUpdateChecker(a.updateCheck))
	}
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
		tui.Actions{Start: attachment.Start, Stop: attachment.Stop, Resume: attachment.Resume, Configure: attachment.Configure, Skip: attachment.Skip, SkipAvailable: attachment.SkipAvailable, SkipLabel: attachment.SkipLabel, SkipTarget: attachment.SkipTarget, OpenCode: attachment.OpenCode, OpenTerminal: attachment.OpenTerminal, SkipChecks: attachment.SkipChecks, FixChecks: attachment.FixChecks, ChecksPaused: attachment.ChecksPaused},
		a.input,
		a.output,
		options...,
	)
}
