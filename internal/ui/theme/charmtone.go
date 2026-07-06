package theme

import (
	"image/color"

	"github.com/charmbracelet/x/exp/charmtone"

	"github.com/morphis/gummi/internal/domain"
)

// GummiDark is the default theme: charmtone's restrained dark base with
// gummy-bear stage accents — the fun lives in the accents, the base
// stays quiet.
func GummiDark() Theme {
	return Theme{
		Name: "gummi-dark",

		Primary:   charmtone.Dolly,   // berry
		Secondary: charmtone.Citron,  // lemon
		Accent:    charmtone.Charple, // grape

		FgBase:   charmtone.Salt,
		FgSubtle: charmtone.Smoke,
		FgMuted:  charmtone.Squid,
		FgFaint:  charmtone.Oyster,

		BgBase:    charmtone.Pepper,
		BgSubtle:  charmtone.BBQ,
		BgSurface: charmtone.Char,
		BgRaised:  charmtone.Iron,

		OnAccent:  charmtone.Butter,
		Separator: charmtone.Char,

		Error:       charmtone.Sriracha,
		Warning:     charmtone.Zest,
		Success:     charmtone.Julep,
		Info:        charmtone.Malibu,
		Destructive: charmtone.Coral,

		StageAccents: map[domain.Stage]color.Color{
			domain.StageTodo:       charmtone.Squid,   // waiting: grey
			domain.StageBrainstorm: charmtone.Charple, // grape
			domain.StageSpec:       charmtone.Malibu,  // blueberry
			domain.StagePlan:       charmtone.Sardine, // ice-blue
			domain.StageTriage:     charmtone.Coral,   // bug: warm salmon
			domain.StageDiagnose:   charmtone.Mustard, // bug: amber
			domain.StageFix:        charmtone.Julep,   // bug: lime (the fix)
			domain.StageImplement:  charmtone.Julep,   // lime
			domain.StageReview:     charmtone.Citron,  // lemon
			domain.StageVerify:     charmtone.Dolly,   // berry
			domain.StageDone:       charmtone.Guac,    // settled green
		},
	}
}
