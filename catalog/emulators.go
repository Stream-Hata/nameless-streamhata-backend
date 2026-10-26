package catalog

type Emulator struct {
	Name      string   `json:"name"`
	URL       string   `json:"url"`
	Platforms []string `json:"platforms"`
	OS        []string `json:"os"`
	Free      bool     `json:"free"`
	Note      string   `json:"note,omitempty"`
}

func emulatorsForPlatform(code string) []Emulator {
	if list, ok := emulators[code]; ok {
		return append([]Emulator(nil), list...)
	}

	if platform, ok := platformByCode(code); ok && platform.PC {
		return nil
	}

	return []Emulator{retroArch}
}

func platformByCode(code string) (Platform, bool) {
	for _, platform := range platforms {
		if platform.Code == code {
			return platform, true
		}
	}

	return Platform{}, false
}

var retroArch = Emulator{
	Name:      "RetroArch",
	URL:       "https://www.retroarch.com/",
	Platforms: []string{"nes", "snes", "genesis", "gb", "gbc", "gba", "n64", "ps1", "saturn", "arcade"},
	OS:        []string{"windows", "macos", "linux", "android", "ios"},
	Free:      true,
}

var emulators = map[string][]Emulator{
	"nes": {
		{Name: "Mesen", URL: "https://www.mesen.ca/", Platforms: []string{"nes", "fds"}, OS: []string{"windows", "linux"}, Free: true},
		{Name: "FCEUX", URL: "https://fceux.com/", Platforms: []string{"nes"}, OS: []string{"windows", "linux"}, Free: true},
	},
	"fds": {
		{Name: "Mesen", URL: "https://www.mesen.ca/", Platforms: []string{"nes", "fds"}, OS: []string{"windows", "linux"}, Free: true},
	},
	"snes": {
		{Name: "Snes9x", URL: "https://www.snes9x.com/", Platforms: []string{"snes"}, OS: []string{"windows", "macos", "linux"}, Free: true},
		{Name: "bsnes", URL: "https://bsnes.dev/", Platforms: []string{"snes"}, OS: []string{"windows", "macos", "linux"}, Free: true},
	},
	"n64": {
		{Name: "Project64", URL: "https://www.pj64-emu.com/", Platforms: []string{"n64"}, OS: []string{"windows"}, Free: true},
		{Name: "Mupen64Plus", URL: "https://mupen64plus.org/", Platforms: []string{"n64"}, OS: []string{"windows", "macos", "linux"}, Free: true},
	},
	"game-boy": {
		{Name: "mGBA", URL: "https://mgba.io/", Platforms: []string{"gb", "gbc", "gba"}, OS: []string{"windows", "macos", "linux"}, Free: true},
	},
	"gbc": {
		{Name: "mGBA", URL: "https://mgba.io/", Platforms: []string{"gb", "gbc", "gba"}, OS: []string{"windows", "macos", "linux"}, Free: true},
	},
	"gba": {
		{Name: "mGBA", URL: "https://mgba.io/", Platforms: []string{"gb", "gbc", "gba"}, OS: []string{"windows", "macos", "linux"}, Free: true},
	},
	"ds": {
		{Name: "melonDS", URL: "https://melonds.kuribo64.net/", Platforms: []string{"ds"}, OS: []string{"windows", "macos", "linux"}, Free: true},
	},
	"3ds": {
		{Name: "Azahar", URL: "https://azahar-emu.org/", Platforms: []string{"3ds"}, OS: []string{"windows", "macos", "linux", "android"}, Free: true},
	},
	"ps1": {
		{Name: "DuckStation", URL: "https://www.duckstation.org/", Platforms: []string{"ps1"}, OS: []string{"windows", "macos", "linux", "android"}, Free: true},
	},
	"ps2": {
		{Name: "PCSX2", URL: "https://pcsx2.net/", Platforms: []string{"ps2"}, OS: []string{"windows", "macos", "linux"}, Free: true},
	},
	"ps3": {
		{Name: "RPCS3", URL: "https://rpcs3.net/", Platforms: []string{"ps3"}, OS: []string{"windows", "macos", "linux"}, Free: true},
	},
	"psp": {
		{Name: "PPSSPP", URL: "https://www.ppsspp.org/", Platforms: []string{"psp"}, OS: []string{"windows", "macos", "linux", "android", "ios"}, Free: true},
	},
	"genesis": {
		{Name: "Kega Fusion", URL: "https://www.carpeludum.com/kega-fusion/", Platforms: []string{"genesis", "master-system", "game-gear", "sega-cd", "32x"}, OS: []string{"windows", "macos", "linux"}, Free: true},
	},
	"master-system": {
		{Name: "Kega Fusion", URL: "https://www.carpeludum.com/kega-fusion/", Platforms: []string{"genesis", "master-system", "game-gear", "sega-cd", "32x"}, OS: []string{"windows", "macos", "linux"}, Free: true},
	},
	"game-gear": {
		{Name: "Kega Fusion", URL: "https://www.carpeludum.com/kega-fusion/", Platforms: []string{"genesis", "master-system", "game-gear", "sega-cd", "32x"}, OS: []string{"windows", "macos", "linux"}, Free: true},
	},
	"sega-cd": {
		{Name: "Kega Fusion", URL: "https://www.carpeludum.com/kega-fusion/", Platforms: []string{"genesis", "master-system", "game-gear", "sega-cd", "32x"}, OS: []string{"windows", "macos", "linux"}, Free: true},
	},
	"32x": {
		{Name: "Kega Fusion", URL: "https://www.carpeludum.com/kega-fusion/", Platforms: []string{"genesis", "master-system", "game-gear", "sega-cd", "32x"}, OS: []string{"windows", "macos", "linux"}, Free: true},
	},
	"saturn": {
		{Name: "Mednafen", URL: "https://mednafen.github.io/", Platforms: []string{"saturn"}, OS: []string{"windows", "macos", "linux"}, Free: true},
	},
	"dreamcast": {
		{Name: "Flycast", URL: "https://www.flycast.org/", Platforms: []string{"dreamcast"}, OS: []string{"windows", "macos", "linux", "android"}, Free: true},
	},
	"gamecube": {
		{Name: "Dolphin", URL: "https://www.dolphin-emu.org/", Platforms: []string{"gamecube", "wii", "wiiware"}, OS: []string{"windows", "macos", "linux", "android"}, Free: true},
	},
	"wii": {
		{Name: "Dolphin", URL: "https://www.dolphin-emu.org/", Platforms: []string{"gamecube", "wii", "wiiware"}, OS: []string{"windows", "macos", "linux", "android"}, Free: true},
	},
	"wiiware": {
		{Name: "Dolphin", URL: "https://www.dolphin-emu.org/", Platforms: []string{"gamecube", "wii", "wiiware"}, OS: []string{"windows", "macos", "linux", "android"}, Free: true},
	},
	"wii-u": {
		{Name: "Cemu", URL: "https://cemu.info/", Platforms: []string{"wii-u"}, OS: []string{"windows", "linux"}, Free: true},
	},
	"xbox": {
		{Name: "xemu", URL: "https://xemu.app/", Platforms: []string{"xbox"}, OS: []string{"windows", "macos", "linux"}, Free: true},
	},
	"3do": {
		{Name: "RetroArch", URL: "https://www.retroarch.com/", Platforms: []string{"3do"}, OS: []string{"windows", "macos", "linux", "android"}, Free: true},
	},
	"jaguar": {
		{Name: "BigPEmu", URL: "https://www.richwhitehouse.com/jaguar/", Platforms: []string{"jaguar"}, OS: []string{"windows"}, Free: true},
	},
	"lynx": {
		{Name: "Mednafen", URL: "https://mednafen.github.io/", Platforms: []string{"lynx"}, OS: []string{"windows", "macos", "linux"}, Free: true},
	},
	"virtual-boy": {
		{Name: "Mednafen", URL: "https://mednafen.github.io/", Platforms: []string{"virtual-boy"}, OS: []string{"windows", "macos", "linux"}, Free: true},
	},
	"atari-2600": {
		{Name: "Stella", URL: "https://stella-emu.github.io/", Platforms: []string{"atari-2600"}, OS: []string{"windows", "macos", "linux"}, Free: true},
	},
	"atari-5200": {
		{Name: "Atari800", URL: "https://atari800.github.io/", Platforms: []string{"atari-5200"}, OS: []string{"windows", "macos", "linux"}, Free: true},
	},
	"atari-7800": {
		{Name: "A7800", URL: "https://github.com/7800-devtools/a7800", Platforms: []string{"atari-7800"}, OS: []string{"windows", "macos", "linux"}, Free: true},
	},
	"c64": {
		{Name: "VICE", URL: "https://vice-emu.sourceforge.io/", Platforms: []string{"c64"}, OS: []string{"windows", "macos", "linux"}, Free: true},
	},
	"amiga": {
		{Name: "FS-UAE", URL: "https://fs-uae.net/", Platforms: []string{"amiga"}, OS: []string{"windows", "macos", "linux"}, Free: true},
	},
	"amstrad-cpc": {
		{Name: "RetroArch", URL: "https://www.retroarch.com/", Platforms: []string{"amstrad-cpc"}, OS: []string{"windows", "macos", "linux", "android"}, Free: true},
	},
	"zx-spectrum": {
		{Name: "RetroArch", URL: "https://www.retroarch.com/", Platforms: []string{"zx-spectrum"}, OS: []string{"windows", "macos", "linux", "android"}, Free: true},
	},
	"msx": {
		{Name: "openMSX", URL: "https://openmsx.org/", Platforms: []string{"msx"}, OS: []string{"windows", "macos", "linux"}, Free: true},
	},
	"x68000": {
		{Name: "RetroArch", URL: "https://www.retroarch.com/", Platforms: []string{"x68000"}, OS: []string{"windows", "macos", "linux", "android"}, Free: true},
	},
	"pc-engine": {
		{Name: "Mednafen", URL: "https://mednafen.github.io/", Platforms: []string{"pc-engine", "turbografx-16", "pc-engine-cd"}, OS: []string{"windows", "macos", "linux"}, Free: true},
	},
	"turbografx-16": {
		{Name: "Mednafen", URL: "https://mednafen.github.io/", Platforms: []string{"pc-engine", "turbografx-16", "pc-engine-cd"}, OS: []string{"windows", "macos", "linux"}, Free: true},
	},
	"pc-engine-cd": {
		{Name: "Mednafen", URL: "https://mednafen.github.io/", Platforms: []string{"pc-engine", "turbografx-16", "pc-engine-cd"}, OS: []string{"windows", "macos", "linux"}, Free: true},
	},
	"sg-1000": {
		{Name: "Kega Fusion", URL: "https://www.carpeludum.com/kega-fusion/", Platforms: []string{"sg-1000"}, OS: []string{"windows", "macos", "linux"}, Free: true},
	},
	"wonderswan": {
		{Name: "Mednafen", URL: "https://mednafen.github.io/", Platforms: []string{"wonderswan"}, OS: []string{"windows", "macos", "linux"}, Free: true},
	},
	"neo-geo-pocket": {
		{Name: "Mednafen", URL: "https://mednafen.github.io/", Platforms: []string{"neo-geo-pocket"}, OS: []string{"windows", "macos", "linux"}, Free: true},
	},
	"java": {
		{Name: "J2ME Loader", URL: "https://github.com/nikita36078/J2ME-Loader", Platforms: []string{"java"}, OS: []string{"android"}, Free: true},
	},
	"dos": {
		{Name: "DOSBox-X", URL: "https://dosbox-x.com/", Platforms: []string{"dos"}, OS: []string{"windows", "macos", "linux"}, Free: true},
	},
}
