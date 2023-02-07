package main

import (
	"context"
	"flag"
	"fmt"
	"golang.org/x/net/html"
	"golang.org/x/time/rate"
	"io"
	"log"
	"net/http"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"time"
)

var myUrl string // server url

func main() {
	var nParallelThreads int // flag var, by default, the maximum number of CPUs
	var url string           // server url
	flag.IntVar(&nParallelThreads, "n", runtime.NumCPU(), "number of parallel threads")
	flag.StringVar(&url, "url", "http://localhost:8080", "server url")
	flag.Parse()
	myUrl = url

	fmt.Println("number of parallel threads:", nParallelThreads)

	rl := rate.NewLimiter(rate.Every(1*time.Second), 10) // Max 10 rps
	client := NewClient(rl)

	ch := make(chan int)
	for i := 0; i <= nParallelThreads; i++ {
		w := NewWorker(i, ch, client)
		go w.startJob()
	}
	for {
		select {
		case v, ok := <-ch:
			if ok {
				fmt.Println("worker id passed:", v)
				if v == 8 {
					return
				}
			}
		default:
			<-time.NewTicker(1 * time.Second).C
		}
	}
}

type Worker struct {
	id       int
	ch       chan int
	client   *RLHTTPClient
	headers  map[string]string
	parsed   []Parsed
	isPassed bool
}

func NewWorker(id int, ch chan int, client *RLHTTPClient) *Worker {
	return &Worker{
		id:       id,
		ch:       ch,
		client:   client,
		headers:  make(map[string]string, 1),
		parsed:   make([]Parsed, 0, 1),
		isPassed: false,
	}
}

type Parsed struct {
	typeString string
	inputs     map[string][]string
}

func (w *Worker) startJob() {
	reqFirst, err := http.NewRequest(http.MethodGet, myUrl, nil)
	if err != nil {
		log.Println(err)
		return
	}
	getFirst, err := w.client.Do(reqFirst)
	if err != nil {
		log.Println(err)
		w.ch <- -1
		return
	}
	defer getFirst.Body.Close()
	cookieHeader := getFirst.Header.Get("Set-Cookie")
	cookies := strings.Split(cookieHeader, ";")
	w.headers["Cookie"] = cookies[0]

	req, err := http.NewRequest(http.MethodGet, myUrl+"/question/"+"1", nil)
	if err != nil {
		log.Println(err)
		return
	}
	req.Header.Set("Cookie", w.headers["Cookie"])
	fmt.Println("Set Cookie", cookies[0])
	getResp, err := w.client.Do(req)
	if err != nil {
		w.ch <- -1
		return
	}
	defer getResp.Body.Close()
	w.work(getResp.Body, 1)
}

func (w *Worker) work(rc io.ReadCloser, iter int) {
	defer rc.Close()
	if w.isPassed {
		return
	}
	body, err := io.ReadAll(rc)
	if err != nil {
		log.Fatal(err)
	}
	dom := html.NewTokenizer(strings.NewReader(string(body)))
	domToken := dom.Token()
	isFormTag := false
	selectId := -1
	id := 0
	radioId := -1
	selectName := ""
loop:
	for {
		tt := dom.Next()
		switch {
		case tt == html.ErrorToken:
			break loop
		case tt == html.StartTagToken:
			domToken = dom.Token()
			if domToken.Data == "select" {
				var val string
				p := Parsed{
					typeString: "select",
					inputs:     make(map[string][]string, 1),
				}
				for _, attr := range domToken.Attr {
					val = attr.Val
				}
				selectName = val

				p.inputs[selectName] = nil
				w.parsed = append(w.parsed, p)
				selectId = id
			}

			var t string
			var name string
			var val string

			for _, attr := range domToken.Attr {
				if attr.Key == "type" {
					if attr.Val == "text" {
						t = attr.Val
					}
				}
				if attr.Key == "value" {
					val = attr.Val
				}
				if attr.Key == "name" {
					name = attr.Val
				}
			}
			if t == "text" {
				m := make(map[string][]string)
				m[name] = []string{val}
				w.parsed = append(w.parsed, Parsed{
					typeString: t,
					inputs:     m,
				})
			}

		case tt == html.EndTagToken:
			domToken = dom.Token()
		case tt == html.TextToken:
			data := domToken.Data
			if data == "title" {
				TxtContent := strings.TrimSpace(html.UnescapeString(string(dom.Text())))
				if len(TxtContent) > 0 {
					if TxtContent == "Test successfully passed" {
						fmt.Printf("%s\n", TxtContent)
						w.isPassed = true
						w.ch <- w.id
						break loop
					}
				}
			}
			if data == "form" {
				isFormTag = true
				continue
			}
			if !isFormTag {
				continue
			}
			if data == "p" {
				id++
			} else if data == "button" {
				break loop
			}

			var typeString string
			var name string
			var val string
			for _, attr := range domToken.Attr {
				if attr.Key == "type" {
					typeString = attr.Val
					if typeString == "radio" {
						radioId = id
					}
				} else if attr.Key == "value" {
					val = attr.Val
				} else if attr.Key == "name" {
					name = attr.Val
				}
			}
			if w.parsed == nil {
				w.parsed = make([]Parsed, 0)
			}

			if data == "option" {
				if w.parsed[selectId].inputs == nil {
					w.parsed[selectId].inputs[selectName] = make([]string, 0, 1)
				}
				w.parsed[selectId].inputs[selectName] = append(w.parsed[selectId].inputs[selectName], val)
			}

			if data == "input" && typeString == "radio" {
				if radioId >= len(w.parsed) {
					m := make(map[string][]string)
					m[name] = []string{val}
					w.parsed = append(w.parsed, Parsed{
						typeString: "radio",
						inputs:     m,
					})
				} else {
					if w.parsed[radioId].inputs == nil {
						w.parsed[radioId].inputs[name] = make([]string, 0, 1)
					}
					w.parsed[radioId].inputs[name] = append(w.parsed[radioId].inputs[name], val)
				}
			}
		}
	}
	if w.isPassed {
		return
	}
	for _, v := range w.parsed {
		fmt.Printf("parsed inputs: %v\n", v)
	}

	values := url.Values{}
	for _, v := range w.parsed {
		if v.typeString == "text" {
			for k, _ := range v.inputs {
				value := "test"
				values.Add(k, value)
				fmt.Printf("input name=%s set %s\n", k, value)
			}
		}
		if v.typeString == "radio" || v.typeString == "select" {
			max := ""
			key := ""
			for k, v1 := range v.inputs {
				key = k
				max = v1[0]
				for i := 1; i < len(v1); i++ {
					if len(max) < len(v1[i]) {
						max = v1[i]
					}
				}
			}
			fmt.Println("input name=", key, " set longest value=", max)
			values.Add(key, max)
		}
	}

	req, err := http.NewRequest(http.MethodPost, myUrl+"/question/"+strconv.FormatInt(int64(iter), 10),
		strings.NewReader(values.Encode()))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", w.headers["Cookie"])

	postReq, err := w.client.Do(req)
	if err != nil {
		log.Println(err)
		return
	}
	if postReq.StatusCode == http.StatusOK {
		fmt.Printf("Question %d passed\n", iter)
	}

	defer postReq.Body.Close()
	iter++
	w.parsed = nil
	w.work(postReq.Body, iter)
}

type RLHTTPClient struct {
	client      *http.Client
	Ratelimiter *rate.Limiter
}

func (c *RLHTTPClient) Do(req *http.Request) (*http.Response, error) {
	ctx := context.Background()
	err := c.Ratelimiter.Wait(ctx)
	if err != nil {
		log.Println(err)
		return nil, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		log.Println(err)
		return nil, err
	}
	return resp, nil
}

func NewClient(rl *rate.Limiter) *RLHTTPClient {
	c := &RLHTTPClient{
		client:      http.DefaultClient,
		Ratelimiter: rl,
	}
	return c
}
