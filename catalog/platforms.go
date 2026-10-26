package catalog

import "strings"

// Platform describes a platform known to the catalog.
type Platform struct {
	Code       string `json:"code"`
	Name       string `json:"name"`
	Icon       string `json:"icon"`
	Maintainer string `json:"maintainer"`
	Era        string `json:"-"`
	Rare       bool   `json:"-"`
	PC         bool   `json:"-"`
}

var platforms = map[string]Platform{
	"Steam":         {Code: "steam", Name: "Steam", Icon: "steam.svg", Maintainer: "Valve", Era: "modern", PC: true},
	"DOS":           {Code: "dos", Name: "DOS", Icon: "", Maintainer: "IBM / Microsoft", Era: "retro"},
	"Windows":       {Code: "windows", Name: "Windows", Icon: "", Maintainer: "Microsoft", Era: "modern", PC: true},
	"PS2":           {Code: "ps2", Name: "PlayStation 2", Icon: "ps2.svg", Maintainer: "Sony", Era: "retro"},
	"PS1":           {Code: "ps1", Name: "PlayStation", Icon: "ps1.svg", Maintainer: "Sony", Era: "retro"},
	"Java":          {Code: "java", Name: "Java ME", Icon: "", Maintainer: "Other", Era: "retro"},
	"DS":            {Code: "ds", Name: "Nintendo DS", Icon: "ds.svg", Maintainer: "Nintendo", Era: "retro"},
	"NES":           {Code: "nes", Name: "Nintendo Entertainment System", Icon: "nes.svg", Maintainer: "Nintendo", Era: "classic"},
	"PS3":           {Code: "ps3", Name: "PlayStation 3", Icon: "ps3.svg", Maintainer: "Sony", Era: "modern"},
	"Amiga":         {Code: "amiga", Name: "Amiga", Icon: "amiga.svg", Maintainer: "Commodore", Era: "classic"},
	"Amstrad CPC":   {Code: "amstrad-cpc", Name: "Amstrad CPC", Icon: "amstradcpc.svg", Maintainer: "Amstrad", Era: "classic"},
	"SNES":          {Code: "snes", Name: "Super Nintendo", Icon: "snes.svg", Maintainer: "Nintendo", Era: "classic"},
	"GBA":           {Code: "gba", Name: "Game Boy Advance", Icon: "gba.svg", Maintainer: "Nintendo", Era: "retro"},
	"PSP":           {Code: "psp", Name: "PlayStation Portable", Icon: "psp.svg", Maintainer: "Sony", Era: "retro"},
	"C64":           {Code: "c64", Name: "Commodore 64", Icon: "c64.svg", Maintainer: "Commodore", Era: "classic"},
	"Wii":           {Code: "wii", Name: "Wii", Icon: "wii.svg", Maintainer: "Nintendo", Era: "modern"},
	"ZX":            {Code: "zx-spectrum", Name: "ZX Spectrum", Icon: "zxspectrum.svg", Maintainer: "Sinclair", Era: "classic"},
	"SMD":           {Code: "genesis", Name: "Sega Mega Drive", Icon: "genesis.svg", Maintainer: "Sega", Era: "classic"},
	"GBC":           {Code: "gbc", Name: "Game Boy Color", Icon: "gbc.svg", Maintainer: "Nintendo", Era: "retro"},
	"GB":            {Code: "game-boy", Name: "Game Boy", Icon: "game-boy.svg", Maintainer: "Nintendo", Era: "classic"},
	"MSX":           {Code: "msx", Name: "MSX", Icon: "", Maintainer: "Other", Era: "classic"},
	"Saturn":        {Code: "saturn", Name: "Sega Saturn", Icon: "sega-saturn.svg", Maintainer: "Sega", Era: "classic", Rare: true},
	"3DS":           {Code: "3ds", Name: "Nintendo 3DS", Icon: "3ds.svg", Maintainer: "Nintendo", Era: "modern"},
	"Xbox":          {Code: "xbox", Name: "Xbox", Icon: "xbox.svg", Maintainer: "Microsoft", Era: "retro"},
	"GC":            {Code: "gamecube", Name: "GameCube", Icon: "gamecube.svg", Maintainer: "Nintendo", Era: "retro"},
	"DC":            {Code: "dreamcast", Name: "Dreamcast", Icon: "dreamcast.svg", Maintainer: "Sega", Era: "retro", Rare: true},
	"Wii U":         {Code: "wii-u", Name: "Wii U", Icon: "wii-u.svg", Maintainer: "Nintendo", Era: "modern"},
	"Sharp X68000":  {Code: "x68000", Name: "Sharp X68000", Icon: "x68000.svg", Maintainer: "Sharp", Era: "classic", Rare: true},
	"Master System": {Code: "master-system", Name: "Master System", Icon: "master-system.svg", Maintainer: "Sega", Era: "classic"},
	"Atari 2600":    {Code: "atari-2600", Name: "Atari 2600", Icon: "atari-2600.svg", Maintainer: "Atari", Era: "classic"},
	"WiiWare":       {Code: "wiiware", Name: "WiiWare", Icon: "wii.svg", Maintainer: "Nintendo", Era: "modern"},
	"N64":           {Code: "n64", Name: "Nintendo 64", Icon: "n64.svg", Maintainer: "Nintendo", Era: "retro"},
	"GG":            {Code: "game-gear", Name: "Game Gear", Icon: "game-gear.svg", Maintainer: "Sega", Era: "retro"},
	"TG16-CD":       {Code: "pc-engine-cd", Name: "TurboGrafx-CD", Icon: "pce-cd.svg", Maintainer: "NEC", Era: "classic", Rare: true},
	"WS":            {Code: "wonderswan", Name: "WonderSwan", Icon: "wonderswan.svg", Maintainer: "Bandai", Era: "retro", Rare: true},
	"TG16":          {Code: "turbografx-16", Name: "TurboGrafx-16", Icon: "tg16.svg", Maintainer: "NEC", Era: "classic", Rare: true},
	"3DO":           {Code: "3do", Name: "3DO", Icon: "3do.svg", Maintainer: "3DO", Era: "classic", Rare: true},
	"Atari Lynx":    {Code: "lynx", Name: "Atari Lynx", Icon: "atarilynx.svg", Maintainer: "Atari", Era: "classic", Rare: true},
	"FDS":           {Code: "fds", Name: "Famicom Disk System", Icon: "famicom.svg", Maintainer: "Nintendo", Era: "classic", Rare: true},
	"Sega CD":       {Code: "sega-cd", Name: "Sega CD", Icon: "segacd.svg", Maintainer: "Sega", Era: "classic", Rare: true},
	"Atari Jaguar":  {Code: "jaguar", Name: "Atari Jaguar", Icon: "atarijaguar.svg", Maintainer: "Atari", Era: "classic", Rare: true},
	"Atari 7800":    {Code: "atari-7800", Name: "Atari 7800", Icon: "atari-7800.svg", Maintainer: "Atari", Era: "classic", Rare: true},
	"SG-1000":       {Code: "sg-1000", Name: "SG-1000", Icon: "sg-1000.svg", Maintainer: "Sega", Era: "classic", Rare: true},
	"Atari 5200":    {Code: "atari-5200", Name: "Atari 5200", Icon: "atari-5200.svg", Maintainer: "Atari", Era: "classic", Rare: true},
	"NGP":           {Code: "neo-geo-pocket", Name: "Neo Geo Pocket", Icon: "ngp.svg", Maintainer: "SNK", Era: "retro", Rare: true},
	"Sega 32X":      {Code: "32x", Name: "Sega 32X", Icon: "sega32x.svg", Maintainer: "Sega", Era: "classic", Rare: true},
	"VB":            {Code: "virtual-boy", Name: "Virtual Boy", Icon: "virtualboy.svg", Maintainer: "Nintendo", Era: "classic", Rare: true},
}

func platformByLabel(label string) Platform {
	label = strings.TrimSpace(label)

	if platform, ok := platforms[label]; ok {
		return platform
	}

	code := slugify(label)
	if code == "" {
		code = "unknown"
	}

	return Platform{Code: code, Name: label, Icon: "", Maintainer: "Other", Era: "retro"}
}

func PlatformList() []Platform {
	result := make([]Platform, 0, len(platforms))

	for _, platform := range platforms {
		result = append(result, platform)
	}

	return result
}
