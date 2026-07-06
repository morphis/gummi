package theme

import (
	"image/color"

	"github.com/charmbracelet/x/exp/charmtone"

	"github.com/morphis/gummi/internal/domain"
)

// GummiLight is the light theme: charmtone's near-white neutrals with
// darkened, saturated accents so every token stays legible on a bright
// background. Same semantic slots as GummiDark — only the values differ.
func GummiLight() Theme {
	return Theme{
		Name: "gummi-light",

		Primary:   charmtone.Grape,  // deep purple reads on white
		Secondary: charmtone.Damson, // deep blue
		Accent:    charmtone.Charple,

		FgBase:   charmtone.Pepper, // darkest ink
		FgSubtle: charmtone.Char,
		FgMuted:  charmtone.Iron,
		FgFaint:  charmtone.Squid,

		BgBase:    charmtone.Salt, // near white
		BgSubtle:  charmtone.Sash,
		BgSurface: charmtone.Sash,
		BgRaised:  charmtone.Smoke,

		OnAccent:  charmtone.Salt, // light text on accent pills
		Separator: charmtone.Smoke,

		Error:       charmtone.Chili,  // dark pink-red
		Warning:     charmtone.Cumin,  // dark mustard (Zest would vanish on white)
		Success:     charmtone.Pickle, // dark green
		Info:        charmtone.Damson, // dark blue
		Destructive: charmtone.Chili,

		StageAccents: map[domain.Stage]color.Color{
			domain.StageTodo:       charmtone.Squid,
			domain.StageBrainstorm: charmtone.Grape,
			domain.StageSpec:       charmtone.Damson,
			domain.StagePlan:       charmtone.Sapphire,
			domain.StageTriage:     charmtone.Coral,
			domain.StageDiagnose:   charmtone.Mustard,
			domain.StageFix:        charmtone.Pickle,
			domain.StageImplement:  charmtone.Pickle,
			domain.StageReview:     charmtone.Cumin,
			domain.StageVerify:     charmtone.Chili,
			domain.StageDone:       charmtone.Gator, // dark settled green
		},
	}
}

// GummiNeon is an alternate dark theme: the same quiet neutrals as
// GummiDark, punched up with cool, high-chroma accents for users who want
// more pop than the default's berry/lemon set.
func GummiNeon() Theme {
	return Theme{
		Name: "gummi-neon",

		Primary:   charmtone.Malibu, // electric blue
		Secondary: charmtone.Turtle, // cyan
		Accent:    charmtone.Guppy,  // bright indigo

		FgBase:   charmtone.Salt,
		FgSubtle: charmtone.Smoke,
		FgMuted:  charmtone.Squid,
		FgFaint:  charmtone.Oyster,

		BgBase:    charmtone.Pepper,
		BgSubtle:  charmtone.BBQ,
		BgSurface: charmtone.Char,
		BgRaised:  charmtone.Iron,

		OnAccent:  charmtone.Pepper,
		Separator: charmtone.Char,

		Error:       charmtone.Cherry,
		Warning:     charmtone.Mustard,
		Success:     charmtone.Julep,
		Info:        charmtone.Sardine,
		Destructive: charmtone.Coral,

		StageAccents: map[domain.Stage]color.Color{
			domain.StageTodo:       charmtone.Oyster,
			domain.StageBrainstorm: charmtone.Guppy,
			domain.StageSpec:       charmtone.Sardine,
			domain.StageImplement:  charmtone.Julep,
			domain.StagePlan:       charmtone.Malibu,
			domain.StageTriage:     charmtone.Coral,
			domain.StageDiagnose:   charmtone.Cumin,
			domain.StageFix:        charmtone.Julep,
			domain.StageReview:     charmtone.Mustard,
			domain.StageVerify:     charmtone.Cherry,
			domain.StageDone:       charmtone.Turtle,
		},
	}
}

// registry maps a theme's short name to its constructor. GummiDark stays
// the default; ByName falls back to it for an unknown name.
var registry = map[string]func() Theme{
	"dark":  GummiDark,
	"light": GummiLight,
	"neon":  GummiNeon,
}

// ByName returns the theme for a short name ("dark"/"light"/"neon"),
// reporting whether the name was recognized; an unknown name yields
// GummiDark so a typo degrades to the default rather than crashing.
func ByName(name string) (Theme, bool) {
	if f, ok := registry[name]; ok {
		return f(), true
	}
	return GummiDark(), false
}

// Names lists the selectable theme names, default first.
func Names() []string { return []string{"dark", "light", "neon"} }
