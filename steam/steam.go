// Package steam — клиент Steam Web API для профиля стримера и библиотеки игр.
package steam

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

type Player struct {
	SteamID     string `json:"steamId"`
	PersonaName string `json:"personaName"`
	ProfileURL  string `json:"profileUrl"`
	Avatar      string `json:"avatar"`
	AvatarFull  string `json:"avatarFull"`
	Status      int    `json:"status"`
	Country     string `json:"country"`
}

type OwnedGame struct {
	AppID           int    `json:"appId"`
	Name            string `json:"name"`
	PlaytimeMinutes int    `json:"playtimeMinutes"`
	IconURL         string `json:"iconUrl"`
}

type Service struct {
	key    string
	client *http.Client

	cacheMu  sync.Mutex
	cache    map[string]cachedAchievements
	schemaMu sync.Mutex
	schemas  map[int]map[string]schemaAchievement

	gamesMu     sync.Mutex
	gamesCache  map[string]cachedGames
	playerMu    sync.Mutex
	playerCache map[string]cachedPlayer
}

type cachedGames struct {
	items   []OwnedGame
	expires time.Time
}

type cachedPlayer struct {
	player  Player
	expires time.Time
}

type cachedAchievements struct {
	items   []Achievement
	expires time.Time
}

func New() *Service {
	return &Service{
		key:         strings.TrimSpace(os.Getenv("STEAM_API_KEY")),
		client:      &http.Client{Timeout: 20 * time.Second},
		cache:       make(map[string]cachedAchievements),
		schemas:     make(map[int]map[string]schemaAchievement),
		gamesCache:  make(map[string]cachedGames),
		playerCache: make(map[string]cachedPlayer),
	}
}

func (s *Service) Configured() bool {
	return s.key != ""
}

func (s *Service) Player(ctx context.Context, steamID string) (Player, error) {
	if !s.Configured() {
		return Player{}, errors.New("STEAM_API_KEY is not configured")
	}

	if strings.TrimSpace(steamID) == "" {
		return Player{}, errors.New("steamId is required")
	}

	s.playerMu.Lock()
	cached, cachedOK := s.playerCache[steamID]
	s.playerMu.Unlock()
	if cachedOK && time.Now().Before(cached.expires) {
		return cached.player, nil
	}

	params := url.Values{}
	params.Set("key", s.key)
	params.Set("steamids", steamID)

	var response struct {
		Response struct {
			Players []struct {
				SteamID     string `json:"steamid"`
				PersonaName string `json:"personaname"`
				ProfileURL  string `json:"profileurl"`
				Avatar      string `json:"avatar"`
				AvatarFull  string `json:"avatarfull"`
				Status      int    `json:"personastate"`
				Country     string `json:"loccountrycode"`
			} `json:"players"`
		} `json:"response"`
	}

	if err := s.get(ctx, "https://api.steampowered.com/ISteamUser/GetPlayerSummaries/v2/?"+params.Encode(), &response); err != nil {
		return Player{}, err
	}

	if len(response.Response.Players) == 0 {
		return Player{}, errors.New("steam player not found")
	}

	player := response.Response.Players[0]

	return Player{
		SteamID:     player.SteamID,
		PersonaName: player.PersonaName,
		ProfileURL:  player.ProfileURL,
		Avatar:      player.Avatar,
		AvatarFull:  player.AvatarFull,
		Status:      player.Status,
		Country:     player.Country,
	}, nil
}

func (s *Service) OwnedGames(ctx context.Context, steamID string) ([]OwnedGame, error) {
	if !s.Configured() {
		return nil, errors.New("STEAM_API_KEY is not configured")
	}

	s.gamesMu.Lock()
	cached, cachedOK := s.gamesCache[steamID]
	s.gamesMu.Unlock()
	if cachedOK && time.Now().Before(cached.expires) {
		return cached.items, nil
	}

	params := url.Values{}
	params.Set("key", s.key)
	params.Set("steamid", steamID)
	params.Set("include_appinfo", "1")
	params.Set("include_played_free_games", "1")

	var response struct {
		Response struct {
			Games []struct {
				AppID           int    `json:"appid"`
				Name            string `json:"name"`
				PlaytimeForever int    `json:"playtime_forever"`
				IconURL         string `json:"img_icon_url"`
			} `json:"games"`
		} `json:"response"`
	}

	if err := s.get(ctx, "https://api.steampowered.com/IPlayerService/GetOwnedGames/v1/?"+params.Encode(), &response); err != nil {
		return nil, err
	}

	games := make([]OwnedGame, 0, len(response.Response.Games))

	for _, game := range response.Response.Games {
		icon := ""

		if game.IconURL != "" {
			icon = fmt.Sprintf("https://media.steampowered.com/steamcommunity/public/images/apps/%d/%s.jpg", game.AppID, game.IconURL)
		}

		games = append(games, OwnedGame{
			AppID:           game.AppID,
			Name:            game.Name,
			PlaytimeMinutes: game.PlaytimeForever,
			IconURL:         icon,
		})
	}

	s.gamesMu.Lock()
	s.gamesCache[steamID] = cachedGames{items: games, expires: time.Now().Add(2 * time.Minute)}
	s.gamesMu.Unlock()

	return games, nil
}

func (s *Service) get(ctx context.Context, target string, targetStruct any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}

	response, err := s.client.Do(request)
	if err != nil {
		return err
	}

	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("steam api status %d", response.StatusCode)
	}

	return json.NewDecoder(response.Body).Decode(targetStruct)
}

type LibraryGame struct {
	OwnedGame
	Achievements   int    `json:"achievements"`
	StreamhataSlug string `json:"streamhataSlug,omitempty"`
}

type InventoryItem struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	IconURL  string `json:"iconUrl"`
	Amount   int    `json:"amount"`
	Tradable bool   `json:"tradable"`
	Market   string `json:"marketHashName,omitempty"`
}

func (s *Service) Library(ctx context.Context, steamID string) ([]LibraryGame, error) {
	games, err := s.OwnedGames(ctx, steamID)
	if err != nil {
		return nil, err
	}

	result := make([]LibraryGame, len(games))
	for index, game := range games {
		result[index] = LibraryGame{OwnedGame: game}
	}

	return result, nil
}

func (s *Service) achievements(ctx context.Context, steamID string, appID int) (int, error) {
	params := url.Values{}
	params.Set("key", s.key)
	params.Set("steamid", steamID)
	params.Set("appid", fmt.Sprintf("%d", appID))

	var response struct {
		PlayerStats struct {
			Achievements []struct {
				Achieved int `json:"achieved"`
			} `json:"achievements"`
		} `json:"playerstats"`
	}

	if err := s.get(ctx, "https://api.steampowered.com/ISteamUserStats/GetPlayerAchievements/v1/?"+params.Encode(), &response); err != nil {
		return 0, err
	}

	count := 0
	for _, achievement := range response.PlayerStats.Achievements {
		if achievement.Achieved == 1 {
			count++
		}
	}

	return count, nil
}

func (s *Service) Inventory(ctx context.Context, steamID string) ([]InventoryItem, error) {
	target := fmt.Sprintf("https://steamcommunity.com/inventory/%s/753/6?l=russian&count=2000", url.PathEscape(steamID))

	var response struct {
		Assets []struct {
			AssetID    string `json:"assetid"`
			ClassID    string `json:"classid"`
			InstanceID string `json:"instanceid"`
			Amount     string `json:"amount"`
		} `json:"assets"`
		Descriptions []struct {
			ClassID    string `json:"classid"`
			InstanceID string `json:"instanceid"`
			Name       string `json:"name"`
			Type       string `json:"type"`
			IconURL    string `json:"icon_url"`
			Tradable   int    `json:"tradable"`
			Market     string `json:"market_hash_name"`
		} `json:"descriptions"`
	}

	if err := s.get(ctx, target, &response); err != nil {
		return nil, err
	}

	type meta struct {
		name     string
		kind     string
		icon     string
		tradable bool
		market   string
	}

	descriptions := make(map[string]meta)

	for _, item := range response.Descriptions {
		descriptions[item.ClassID+":"+item.InstanceID] = meta{
			name:     item.Name,
			kind:     item.Type,
			icon:     item.IconURL,
			tradable: item.Tradable == 1,
			market:   item.Market,
		}
	}

	result := make([]InventoryItem, 0, len(response.Assets))

	for _, asset := range response.Assets {
		info := descriptions[asset.ClassID+":"+asset.InstanceID]
		amount := 0
		_, _ = fmt.Sscanf(asset.Amount, "%d", &amount)
		if amount <= 0 {
			amount = 1
		}

		icon := ""
		if info.icon != "" {
			icon = "https://community.cloudflare.steamstatic.com/economy/image/" + info.icon + "/96fx96f"
		}

		result = append(result, InventoryItem{
			ID:       asset.AssetID,
			Name:     info.name,
			Type:     info.kind,
			IconURL:  icon,
			Amount:   amount,
			Tradable: info.tradable,
			Market:   info.market,
		})
	}

	return result, nil
}

type Achievement struct {
	AppID       int    `json:"appId"`
	GameName    string `json:"gameName"`
	APIName     string `json:"apiName"`
	Name        string `json:"name"`
	Description string `json:"description"`
	IconURL     string `json:"iconUrl"`
	Achieved    bool   `json:"achieved"`
	UnlockTime  int64  `json:"unlockTime"`
}

type schemaAchievement struct {
	DisplayName string `json:"displayName"`
	Description string `json:"description"`
	Icon        string `json:"icon"`
}

func (s *Service) Achievements(ctx context.Context, steamID string) ([]Achievement, error) {
	s.cacheMu.Lock()
	cached, exists := s.cache[steamID]
	s.cacheMu.Unlock()

	if exists && time.Now().Before(cached.expires) {
		return cached.items, nil
	}

	games, err := s.OwnedGames(ctx, steamID)
	if err != nil {
		return nil, err
	}

	if len(games) > 120 {
		games = games[:120]
	}

	result := make([]Achievement, 0, 256)
	mu := sync.Mutex{}
	sem := make(chan struct{}, 6)
	var wg sync.WaitGroup

	for _, game := range games {
		game := game
		wg.Add(1)

		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			items, err := s.playerAchievements(ctx, steamID, game)
			if err != nil {
				return
			}

			mu.Lock()
			result = append(result, items...)
			mu.Unlock()
		}()
	}

	wg.Wait()

	s.cacheMu.Lock()
	s.cache[steamID] = cachedAchievements{items: result, expires: time.Now().Add(2 * time.Minute)}
	s.cacheMu.Unlock()

	return result, nil
}

func (s *Service) playerAchievements(ctx context.Context, steamID string, game OwnedGame) ([]Achievement, error) {
	params := url.Values{}
	params.Set("key", s.key)
	params.Set("steamid", steamID)
	params.Set("appid", fmt.Sprintf("%d", game.AppID))
	params.Set("l", "russian")

	var response struct {
		PlayerStats struct {
			Achievements []struct {
				APIName    string `json:"apiname"`
				Achieved   int    `json:"achieved"`
				UnlockTime int64  `json:"unlocktime"`
			} `json:"achievements"`
		} `json:"playerstats"`
	}

	if err := s.get(ctx, "https://api.steampowered.com/ISteamUserStats/GetPlayerAchievements/v1/?"+params.Encode(), &response); err != nil {
		return nil, err
	}

	schema := s.schema(ctx, game.AppID)
	result := make([]Achievement, 0, len(response.PlayerStats.Achievements))

	for _, item := range response.PlayerStats.Achievements {
		meta := schema[item.APIName]
		name := meta.DisplayName
		if name == "" {
			name = item.APIName
		}

		result = append(result, Achievement{
			AppID:       game.AppID,
			GameName:    game.Name,
			APIName:     item.APIName,
			Name:        name,
			Description: meta.Description,
			IconURL:     meta.Icon,
			Achieved:    item.Achieved == 1,
			UnlockTime:  item.UnlockTime,
		})
	}

	return result, nil
}

func (s *Service) schema(ctx context.Context, appID int) map[string]schemaAchievement {
	s.schemaMu.Lock()
	cached, exists := s.schemas[appID]
	s.schemaMu.Unlock()

	if exists {
		return cached
	}

	params := url.Values{}
	params.Set("key", s.key)
	params.Set("appid", fmt.Sprintf("%d", appID))
	params.Set("l", "russian")

	var response struct {
		Game struct {
			AvailableGameStats struct {
				Achievements []struct {
					Name        string `json:"name"`
					DisplayName string `json:"displayName"`
					Description string `json:"description"`
					Icon        string `json:"icon"`
				} `json:"achievements"`
			} `json:"availableGameStats"`
		} `json:"game"`
	}

	result := make(map[string]schemaAchievement)

	if err := s.get(ctx, "https://api.steampowered.com/ISteamUserStats/GetSchemaForGame/v2/?"+params.Encode(), &response); err == nil {
		for _, item := range response.Game.AvailableGameStats.Achievements {
			result[item.Name] = schemaAchievement{
				DisplayName: item.DisplayName,
				Description: item.Description,
				Icon:        item.Icon,
			}
		}
	}

	s.schemaMu.Lock()
	s.schemas[appID] = result
	s.schemaMu.Unlock()

	return result
}
