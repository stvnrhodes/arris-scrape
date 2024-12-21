package main

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"os"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/net/html"
)

type downstreamChannel struct {
	ChannelID      string
	LockStatus     string
	Modulation     string
	FrequencyHz    int64
	PowerdBmV      float64
	SNRMERdB       float64
	Corrected      int
	Uncorrectables int
}

type upstreamChannel struct {
	Channel     string
	ChannelID   string
	LockStatus  string
	ChannelType string
	FrequencyHz int64
	WidthHz     int64
	PowerdBmV   float64
}

func findTextNode(node *html.Node, text string) *html.Node {
	if node == nil {
		return nil
	}
	if node.Type == html.TextNode && node.Data == text {
		return node
	}
	if n := findTextNode(node.FirstChild, text); n != nil {
		return n
	}
	if n := findTextNode(node.NextSibling, text); n != nil {
		return n
	}
	return nil
}

func scrapeTable(rowPtr *html.Node) [][]string {
	var scraped [][]string
	for rowPtr != nil {
		if len(rowPtr.Attr) == 1 && rowPtr.Attr[0].Key == "align" && rowPtr.Attr[0].Val == "left" {
			var vals []string
			columnPtr := rowPtr.FirstChild
			for columnPtr != nil {
				if columnPtr.Data == "td" {
					vals = append(vals, columnPtr.FirstChild.Data)
				}
				columnPtr = columnPtr.NextSibling
			}
			scraped = append(scraped, vals)
		}
		rowPtr = rowPtr.NextSibling
	}
	return scraped
}
func parseDownstream(page *html.Node) ([]downstreamChannel, error) {
	var data []downstreamChannel
	tableTitle := findTextNode(page, "Downstream Bonded Channels")
	if tableTitle == nil {
		return nil, fmt.Errorf("unable to find downstream bonded channels table")
	}
	for _, row := range scrapeTable(tableTitle.Parent.Parent.Parent) {
		frequencyHz, err := strconv.ParseInt(strings.Split(row[3], " ")[0], 10, 64)
		if err != nil {
			return nil, err
		}
		powerdBmV, err := strconv.ParseFloat(strings.Split(row[4], " ")[0], 64)
		if err != nil {
			return nil, err
		}
		snrMERdB, err := strconv.ParseFloat(strings.Split(row[5], " ")[0], 64)
		if err != nil {
			return nil, err
		}
		corrected, err := strconv.Atoi(row[6])
		if err != nil {
			return nil, err
		}
		uncorrectables, err := strconv.Atoi(row[7])
		if err != nil {
			return nil, err
		}
		data = append(data, downstreamChannel{
			ChannelID:      row[0],
			LockStatus:     row[1],
			Modulation:     row[2],
			FrequencyHz:    frequencyHz,
			PowerdBmV:      powerdBmV,
			SNRMERdB:       snrMERdB,
			Corrected:      corrected,
			Uncorrectables: uncorrectables,
		})
	}
	return data, nil
}
func parseUpstream(page *html.Node) ([]upstreamChannel, error) {
	var data []upstreamChannel
	tableTitle := findTextNode(page, "Upstream Bonded Channels")
	if tableTitle == nil {
		return nil, fmt.Errorf("unable to find upstream bonded channels table")
	}
	for _, row := range scrapeTable(tableTitle.Parent.Parent.Parent) {
		frequencyHz, err := strconv.ParseInt(strings.Split(row[4], " ")[0], 10, 64)
		if err != nil {
			return nil, err
		}
		widthHz, err := strconv.ParseInt(strings.Split(row[5], " ")[0], 10, 64)
		if err != nil {
			return nil, err
		}
		powerdBmV, err := strconv.ParseFloat(strings.Split(row[6], " ")[0], 64)
		if err != nil {
			return nil, err
		}
		data = append(data, upstreamChannel{
			Channel:     row[0],
			ChannelID:   row[1],
			LockStatus:  row[2],
			ChannelType: row[3],
			FrequencyHz: frequencyHz,
			WidthHz:     widthHz,
			PowerdBmV:   powerdBmV,
		})
	}
	return data, nil
}

func (f *fetcher) fetchPage(ctx context.Context) (*html.Node, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.token != "" {
		// Try logging in with the token we already have
		page, err := f.fetchPageInner(ctx)
		if err != nil {
			return nil, err
		}
		if findTextNode(page, "Login") == nil {
			return page, nil
		}
	}

	// Start off with a login page request. An auth request will only
	// succeed after a login page has been presented.
	authURL := "https://" + f.addr + "/cmconnectionstatus.html?login_" + base64.URLEncoding.EncodeToString([]byte(f.username+":"+f.passwd))
	loginPageReq, err := http.NewRequestWithContext(ctx, "GET", "https://"+f.addr, nil)
	if err != nil {
		return nil, err
	}
	if _, err = f.client.Do(loginPageReq); err != nil {
		return nil, err
	}
	// After the login page, poke at auth directly
	authReq, err := http.NewRequestWithContext(ctx, "GET", authURL, nil)
	if err != nil {
		return nil, err
	}
	authReq.SetBasicAuth(f.username, f.passwd)
	authResp, err := f.client.Do(authReq)
	if err != nil {
		return nil, err
	}
	log.Print("authenticated to modem")
	token, err := io.ReadAll(authResp.Body)
	if err != nil {
		return nil, err
	}
	f.token = string(token)
	return f.fetchPageInner(ctx)
}

func (f *fetcher) fetchPageInner(ctx context.Context) (*html.Node, error) {
	url := "https://" + f.addr + "/cmconnectionstatus.html?ct_" + f.token
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, err
	}
	return html.Parse(resp.Body)
}

type fetcher struct {
	addr, username, passwd string
	client                 *http.Client
	mu                     sync.Mutex
	token                  string
}

func newFetcher(addr, username, passwd string) (*fetcher, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{
		Jar: jar,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	return &fetcher{addr: addr, username: username, passwd: passwd, client: client}, nil
}

func (f *fetcher) writeMetrics(ctx context.Context, w io.Writer) error {
	page, err := f.fetchPage(ctx)
	if err != nil {
		return err
	}
	if findTextNode(page, "Login") != nil {
		return errors.New("Unable to get past login page")
	}
	downstream, err := parseDownstream(page)
	if err != nil {
		return err
	}
	for _, d := range downstream {
		// Print everything in Prometheus format, float64 only
		fmt.Fprintf(w, "downstream_bonded_channels_frequency_hz{channel_id=%q} %v\n", d.ChannelID, d.FrequencyHz)
		fmt.Fprintf(w, "downstream_bonded_channels_power_dbmv{channel_id=%q} %v\n", d.ChannelID, d.PowerdBmV)
		fmt.Fprintf(w, "downstream_bonded_channels_snr_mer_db{channel_id=%q} %v\n", d.ChannelID, d.SNRMERdB)
		fmt.Fprintf(w, "downstream_bonded_channels_corrected{channel_id=%q} %v\n", d.ChannelID, d.Corrected)
		fmt.Fprintf(w, "downstream_bonded_channels_uncorrectables{channel_id=%q} %v\n", d.ChannelID, d.Uncorrectables)
	}
	upstream, err := parseUpstream(page)
	if err != nil {
		return err
	}
	for _, u := range upstream {
		// Print everything in Prometheus format, float64 only
		fmt.Fprintf(w, "upstream_bonded_channels_frequency_hz{channel_id=%q} %v\n", u.ChannelID, u.FrequencyHz)
		fmt.Fprintf(w, "upstream_bonded_channels_width_hz{channel_id=%q} %v\n", u.ChannelID, u.WidthHz)
		fmt.Fprintf(w, "upstream_bonded_channels_power_dbmv{channel_id=%q} %v\n", u.ChannelID, u.PowerdBmV)
	}
	return nil
}

func main() {
	ctx := context.Background()
	addr := flag.String("modem-addr", "192.168.100.1", "Modem address")
	username := flag.String("username", "admin", "Modem username")
	passwd := flag.String("passwd", os.Getenv("MODEM_PASSWD"), "Modem password")
	httpAddr := flag.String("http-addr", "", "Address like 0.0.0.0:1234. If provided, will run in server mode")
	flag.Parse()

	fetcher, err := newFetcher(*addr, *username, *passwd)
	if err != nil {
		log.Fatal(err)
	}
	if *httpAddr != "" {
		log.Printf("serving on %v", *httpAddr)
		http.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
			if err := fetcher.writeMetrics(r.Context(), w); err != nil {
				log.Print(err)
			}
			log.Print("successfully fetched metrics")
		})
		log.Fatal(http.ListenAndServe(*httpAddr, nil))
	}
	if err := fetcher.writeMetrics(ctx, os.Stdout); err != nil {
		log.Fatal(err)
	}
}
