package main

// Thin client for MultiViewer's local GraphQL API.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

var apiURL = func() string {
	if u := os.Getenv("MULTIVIEWER_URL"); u != "" {
		return u
	}
	return "http://localhost:10101/api/graphql"
}()

var httpClient = &http.Client{Timeout: 3 * time.Second}

// Values are written into queries through these encoders only, so nothing coming
// from the network can change the shape of a query.
func gqlString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func gqlBool(b bool) string { return strconv.FormatBool(b) }

func gqlNumber(f float64) (string, error) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return "", fmt.Errorf("invalid number: %v", f)
	}
	return strconv.FormatFloat(f, 'f', -1, 64), nil
}

func gql(query string, out any) error {
	body, _ := json.Marshal(map[string]string{"query": query})
	res, err := httpClient.Post(apiURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer res.Body.Close()
	var payload struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
		return err
	}
	if len(payload.Errors) > 0 {
		msgs := make([]string, len(payload.Errors))
		for i, e := range payload.Errors {
			msgs[i] = e.Message
		}
		return errors.New(strings.Join(msgs, "; "))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(payload.Data, out)
}

type player struct {
	ID         string
	Onboard    bool // a driver onboard window
	Label      string
	Paused     bool
	Live       bool
	Time       float64
	SessionKey string
}

// getPlayers returns all open MultiViewer players.
func getPlayers() ([]player, error) {
	var data struct {
		Players []struct {
			ID    string `json:"id"`
			State struct {
				Paused                  bool    `json:"paused"`
				Live                    bool    `json:"live"`
				InterpolatedCurrentTime float64 `json:"interpolatedCurrentTime"`
			} `json:"state"`
			DriverData *struct {
				TLA string `json:"tla"`
			} `json:"driverData"`
			StreamData *struct {
				SessionKey json.RawMessage `json:"sessionKey"`
				Title      string          `json:"title"`
			} `json:"streamData"`
		} `json:"players"`
	}
	err := gql(`{ players { id state { paused live interpolatedCurrentTime } driverData { tla } streamData { contentId sessionKey title } } }`, &data)
	if err != nil {
		return nil, err
	}
	players := make([]player, 0, len(data.Players))
	for _, p := range data.Players {
		pl := player{
			ID:      p.ID,
			Onboard: p.DriverData != nil,
			Label:   "player",
			Paused:  p.State.Paused,
			Live:    p.State.Live,
			Time:    p.State.InterpolatedCurrentTime,
		}
		if p.StreamData != nil {
			pl.SessionKey = strings.Trim(string(p.StreamData.SessionKey), `"`) // string or number
			if pl.SessionKey == "null" {
				pl.SessionKey = ""
			}
			if p.StreamData.Title != "" {
				pl.Label = p.StreamData.Title
			}
		}
		if p.DriverData != nil && p.DriverData.TLA != "" {
			pl.Label = p.DriverData.TLA
		}
		players = append(players, pl)
	}
	return players, nil
}

func setPaused(id string, paused bool) error {
	return gql(fmt.Sprintf("mutation { playerSetPaused(id:%s, paused:%s) }", gqlString(id), gqlBool(paused)), nil)
}

func seekTo(id string, absolute float64) error {
	n, err := gqlNumber(absolute)
	if err != nil {
		return err
	}
	return gql(fmt.Sprintf("mutation { playerSeekTo(id:%s, absolute:%s) }", gqlString(id), n), nil)
}

// syncOthersTo is MultiViewer's own "sync": aligns the other players (onboards) and the live timing to this player.
func syncOthersTo(id string) error {
	return gql(fmt.Sprintf("mutation { playerSync(id:%s) }", gqlString(id)), nil)
}
