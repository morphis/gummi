# Demo tapes

These are [vhs](https://github.com/charmbracelet/vhs) `.tape` scripts — the
reproducible source for gummi's demo GIFs. They are checked in as source;
rendering them produces the GIFs.

## Rendering

Requires `vhs` and its toolchain (`ttyd`, `ffmpeg`):

```sh
go install github.com/charmbracelet/vhs@latest   # needs ttyd + ffmpeg on PATH
make build                                        # gummi on ./bin/gummi
vhs docs/demos/board-tour.tape                     # → docs/demos/board-tour.gif
vhs docs/demos/spend-plan.tape                      # → docs/demos/spend-plan.gif
```

Each tape sets up a throwaway demo repo in a hidden block, then drives the
real `gummi board` TUI. They mirror the live PTY drives used to verify
each milestone (see `docs/captures/*.ansi` for static frames captured in
CI where a GIF toolchain isn't available).

> Note: the GIFs are not committed to the repo; render them locally or in
> a docs-publishing job. This environment lacks the vhs toolchain, so only
> the tape sources ship here.
