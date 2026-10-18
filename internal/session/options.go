package session

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"strings"
)

func (s *Store) configure(st *State, o StartOptions, previous *State) error {
	if previous != nil {
		st.RenderListen = previous.RenderListen
		st.AuthToken = previous.AuthToken
		st.EmbedOrigins = append([]string(nil), previous.EmbedOrigins...)
		st.DownloadDir = previous.DownloadDir
		st.Proxy = previous.Proxy
		st.ProxyBypass = previous.ProxyBypass
		st.ProxyUsername = previous.ProxyUsername
		st.ProxyPassword = previous.ProxyPassword
	}
	if o.RenderListen != "" {
		st.RenderListen = o.RenderListen
	}
	if st.RenderListen == "" {
		st.RenderListen = "127.0.0.1:0"
	}
	if o.AuthToken != "" {
		if o.AuthToken == "auto" {
			if st.AuthToken == "" {
				b := make([]byte, 32)
				if _, e := rand.Read(b); e != nil {
					return e
				}
				st.AuthToken = hex.EncodeToString(b)
			}
		} else {
			st.AuthToken = o.AuthToken
		}
	}
	host, _, err := net.SplitHostPort(st.RenderListen)
	if err != nil {
		return fmt.Errorf("invalid --listen: %w", err)
	}
	ip := net.ParseIP(host)
	local := host == "localhost" || (ip != nil && ip.IsLoopback())
	if !local && st.AuthToken == "" {
		return errors.New("non-loopback --listen requires --auth-token TOKEN|auto")
	}
	if o.EmbedOrigins != nil {
		st.EmbedOrigins = append([]string(nil), o.EmbedOrigins...)
	}
	for _, origin := range st.EmbedOrigins {
		u, e := url.Parse(origin)
		if e != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
			return fmt.Errorf("invalid embed origin %q; use scheme://host[:port]", origin)
		}
	}
	if o.DownloadDir != "" {
		st.DownloadDir = o.DownloadDir
	}
	if st.DownloadDir == "" {
		st.DownloadDir = filepath.Join(s.Dir, "downloads")
	}
	st.DownloadDir, err = filepath.Abs(st.DownloadDir)
	if err != nil {
		return err
	}
	if o.Proxy != "" {
		st.ProxyUsername = ""
		st.ProxyPassword = ""
		st.Proxy = ""
		if o.Proxy != "direct" {
			raw := o.Proxy
			if !strings.Contains(raw, "://") {
				raw = "http://" + raw
			}
			u, e := url.Parse(raw)
			if e != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "socks4" && u.Scheme != "socks5") || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
				return errors.New("proxy must use http, https, socks4 or socks5 with a host and optional port")
			}
			if u.User != nil {
				if strings.HasPrefix(u.Scheme, "socks") {
					return errors.New("Chromium SOCKS proxy authentication is unsupported; use an authenticated HTTP proxy")
				}
				st.ProxyUsername = u.User.Username()
				if st.ProxyUsername == "" {
					return errors.New("proxy username cannot be empty")
				}
				st.ProxyPassword, _ = u.User.Password()
				u.User = nil
			}
			u.Path = ""
			st.Proxy = u.String()
		}
	}
	if !o.Render && st.ProxyUsername != "" {
		return errors.New("authenticated proxy requires the controller; omit --no-render")
	}
	if o.ProxyBypass != "" {
		st.ProxyBypass = strings.ReplaceAll(o.ProxyBypass, ",", ";")
	}
	return nil
}
func configEqual(a, b *State) bool {
	return a.RenderListen == b.RenderListen && a.AuthToken == b.AuthToken && a.DownloadDir == b.DownloadDir && a.Proxy == b.Proxy && a.ProxyBypass == b.ProxyBypass && a.ProxyUsername == b.ProxyUsername && a.ProxyPassword == b.ProxyPassword && strings.Join(a.EmbedOrigins, "\n") == strings.Join(b.EmbedOrigins, "\n")
}
func copyConfig(dst, src *State) {
	dst.RenderListen = src.RenderListen
	dst.AuthToken = src.AuthToken
	dst.EmbedOrigins = src.EmbedOrigins
	dst.DownloadDir = src.DownloadDir
	dst.Proxy = src.Proxy
	dst.ProxyBypass = src.ProxyBypass
	dst.ProxyUsername = src.ProxyUsername
	dst.ProxyPassword = src.ProxyPassword
}
func controllerConfig(st *State) []byte { data, _ := json.Marshal(st); return data }
