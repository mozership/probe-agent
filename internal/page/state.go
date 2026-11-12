package page

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/storage"
	"github.com/chromedp/chromedp"
)

// StorageState is what the browser starts with: cookies, and the
// localStorage of origins. It has the shape of Playwright's storage state,
// as the save_storage_state action of the browser action writes it, so
// that a login another step saved lets the agent start signed in.
type StorageState struct {
	Cookies []StateCookie `json:"cookies"`
	Origins []StateOrigin `json:"origins"`
}

// StateCookie is a cookie of a storage state.
type StateCookie struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Domain string `json:"domain"`
	Path   string `json:"path"`
	// Expires is in seconds since the UNIX epoch, and -1 for a cookie that
	// ends with the browser session.
	Expires  float64 `json:"expires"`
	HTTPOnly bool    `json:"httpOnly"`
	Secure   bool    `json:"secure"`
	SameSite string  `json:"sameSite,omitempty"`
}

// StateOrigin is the localStorage of an origin.
type StateOrigin struct {
	Origin       string      `json:"origin"`
	LocalStorage []StateItem `json:"localStorage"`
}

// StateItem is an item of localStorage.
type StateItem struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// ReadStorageState reads the storage state of the file at path.
func ReadStorageState(path string) (*StorageState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("storage_state: %w", err)
	}
	var s StorageState
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("storage_state %s is not a storage state: %w", path, err)
	}
	return &s, nil
}

// loadStorageState returns an action that gives the browser the cookies and
// the localStorage of s, before any page is opened.
func loadStorageState(s *StorageState) chromedp.Action[chromedp.Void] {
	return func(ctx context.Context, t *chromedp.Target) (chromedp.Void, error) {
		if len(s.Cookies) > 0 {
			cookies := make([]*network.CookieParam, len(s.Cookies))
			for i, c := range s.Cookies {
				cookies[i] = &network.CookieParam{
					Name:     c.Name,
					Value:    c.Value,
					Domain:   c.Domain,
					Path:     c.Path,
					Secure:   c.Secure,
					HTTPOnly: c.HTTPOnly,
					SameSite: network.CookieSameSite(c.SameSite),
				}
				// A cookie of the session has no expiry to give.
				if c.Expires > 0 {
					cookies[i].Expires = cdp.TimeSinceEpoch(c.Expires)
				}
			}
			if _, err := chromedp.CallBrowser(ctx, storage.SetCookies, storage.SetCookiesParams{Cookies: cookies}); err != nil {
				return chromedp.Void{}, fmt.Errorf("storage_state: setting the cookies: %w", err)
			}
		}
		if err := setLocalStorage(ctx, t, s.Origins); err != nil {
			return chromedp.Void{}, fmt.Errorf("storage_state: %w", err)
		}
		return chromedp.Void{}, nil
	}
}

// setLocalStorage gives each origin its localStorage. A page can write the
// localStorage of its own origin only, so a page of each origin is opened
// in turn, as Playwright does. Its requests are answered with an empty page
// here rather than sent, so that the application is not asked for a page
// and runs no script, and the tab is left on about:blank, as it starts.
// blankPage answers the pages of the origins. Its icon is in it, as
// otherwise the browser asks the origin for /favicon.ico once the requests
// are no longer answered here.
const blankPage = `<!doctype html><html><head><link rel="icon" href="data:,"></head></html>`

func setLocalStorage(ctx context.Context, t *chromedp.Target, origins []StateOrigin) error {
	// An origin with nothing to keep, as of a login kept in cookies only,
	// needs no page opened.
	var filled []StateOrigin
	for _, o := range origins {
		if len(o.LocalStorage) > 0 {
			filled = append(filled, o)
		}
	}
	if len(filled) == 0 {
		return nil
	}

	lctx, cancel := context.WithCancel(ctx)
	paused := cdp.Events(lctx, t, fetch.RequestPaused)
	if _, err := cdp.Call(ctx, t, fetch.Enable, fetch.EnableParams{}); err != nil {
		cancel()
		return fmt.Errorf("answering the requests for the origins: %w", err)
	}
	answered := make(chan struct{})
	go func() {
		defer close(answered)
		for ev, err := range paused {
			if err != nil {
				return
			}
			_, _ = cdp.Call(lctx, t, fetch.FulfillRequest, fetch.FulfillRequestParams{
				RequestID:       ev.RequestID,
				ResponseCode:    200,
				ResponseHeaders: []*fetch.HeaderEntry{{Name: "Content-Type", Value: "text/html"}},
				Body:            []byte(blankPage),
			})
		}
	}()
	stop := func() error {
		_, err := cdp.Call(ctx, t, fetch.Disable, cdp.Empty{})
		cancel()
		<-answered
		return err
	}

	for _, o := range filled {
		items, err := json.Marshal(o.LocalStorage)
		if err != nil {
			_ = stop()
			return err
		}
		script := fmt.Sprintf(`((items) => { for (const { name, value } of items) { localStorage.setItem(name, value); } })(%s)`, items)
		if _, err := chromedp.Navigate(o.Origin+"/")(ctx, t); err != nil {
			_ = stop()
			return fmt.Errorf("opening %s to set its localStorage: %w", o.Origin, err)
		}
		if _, err := chromedp.Evaluate[chromedp.Void](script)(ctx, t); err != nil {
			_ = stop()
			return fmt.Errorf("setting localStorage of %s: %w", o.Origin, err)
		}
	}

	if err := stop(); err != nil {
		return fmt.Errorf("answering the requests for the origins: %w", err)
	}
	_, err := chromedp.Navigate("about:blank")(ctx, t)
	return err
}
