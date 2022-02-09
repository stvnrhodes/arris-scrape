package main

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"
)

type downstreamChannel struct {
	ChannelID      int
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
	ChannelID   int
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
		channelID, err := strconv.Atoi(row[0])
		if err != nil {
			return nil, err
		}
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
			ChannelID:      channelID,
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
		channelID, err := strconv.Atoi(row[1])
		if err != nil {
			return nil, err
		}
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
			ChannelID:   channelID,
			LockStatus:  row[2],
			ChannelType: row[3],
			FrequencyHz: frequencyHz,
			WidthHz:     widthHz,
			PowerdBmV:   powerdBmV,
		})
	}
	return data, nil
}

func fetchPage(ctx context.Context, addr, username, passwd string) (*html.Node, error) {
	baseURL := "https://" + addr + "/cmconnectionstatus.html"
	authURL := baseURL + "?login_" + base64.URLEncoding.EncodeToString([]byte(username+":"+passwd))
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{
		Jar: jar,
		Transport: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout:   30 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			MaxIdleConns:          100,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			TLSClientConfig:       &tls.Config{InsecureSkipVerify: true},
		},
	}
	authReq, err := http.NewRequestWithContext(ctx, "GET", authURL, nil)
	if err != nil {
		return nil, err
	}
	authReq.SetBasicAuth(username, passwd)
	authResp, err := client.Do(authReq)
	if err != nil {
		return nil, err
	}
	token, err := ioutil.ReadAll(authResp.Body)
	if err != nil {
		return nil, err
	}
	url := baseURL + "?ct_" + string(token)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	return html.Parse(resp.Body)
}

func main() {
	ctx := context.Background()
	addr := flag.String("addr", "192.168.100.1", "Modem address")
	username := flag.String("username", "admin", "Modem username")
	passwd := flag.String("passwd", os.Getenv("MODEM_PASSWD"), "Modem password")
	flag.Parse()

	page, err := fetchPage(ctx, *addr, *username, *passwd)
	if err != nil {
		log.Fatal(err)
	}
	if findTextNode(page, "Login") != nil {
		log.Fatal("Unable to get past login page")
	}
	downstream, err := parseDownstream(page)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(downstream)
	fmt.Println(parseUpstream(page))
}
