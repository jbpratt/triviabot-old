package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io/ioutil"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"nhooyr.io/websocket"
)

const addr = "wss://chat.strims.gg/ws"

// TODO: query param
var triviaURL = "https://opentdb.com/api.php?amount=10&difficulty=easy"

type response struct {
	ResponseCode int      `json:"response_code"`
	Results      []result `json:"results"`
}

type result struct {
	Category         string   `json:"category"`
	Type             string   `json:"type"`
	Difficulty       string   `json:"difficulty"`
	Question         string   `json:"question"`
	CorrectAnswer    string   `json:"correct_answer"`
	IncorrectAnswers []string `json:"incorrect_answers"`
}

func main() {
	inProgress := false

	triviaClient := http.Client{Timeout: time.Second * 2}

	jwt := os.Getenv("STRIMS_TOKEN")
	if jwt == "" {
		panic(fmt.Errorf("no jwt provided"))
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := requestToken(); err != nil {
		panic(err)
	}

	c, _, err := websocket.Dial(ctx, addr,
		&websocket.DialOptions{
			HTTPHeader: http.Header{
				"Cookie": []string{fmt.Sprintf("jwt=%s", jwt)},
			},
		})
	if err != nil {
		panic(err)
	}
	defer c.Close(websocket.StatusInternalError, "connection closed")

	fmt.Println("Connected to chat...")
	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			panic(err)
		}

		msg := strings.SplitN(string(data), " ", 2)
		var content map[string]interface{}
		if msg[0] == "MSG" {
			if err = json.Unmarshal([]byte(msg[1]), &content); err != nil {
				panic(err)
			}
			chatMsg := content["data"].(string)
			if strings.HasPrefix(chatMsg, "!trivia") && !inProgress {
				// check if msg contains category

				fmt.Println("Starting trivia round")
				inProgress = true
				fmt.Println("Requesting data")
				q, err := requestTriviaData(ctx, &triviaClient)
				if err != nil {
					panic(err)
				}
				answers := []string{q.CorrectAnswer}
				answers = append(answers, q.IncorrectAnswers...)

				var out string
				for i, ans := range answers {
					out += fmt.Sprintf("`%d` %s ", i+1, strings.ReplaceAll(ans, "\"", "'"))
				}

				rand.Seed(time.Now().UnixNano())
				rand.Shuffle(len(answers), func(i, j int) { answers[i], answers[j] = answers[j], answers[i] })
				x := fmt.Sprintf(
					"Trivia time! (%s) Question: `%s`... Possible answers: %s (answer in 20s)",
					q.Category, strings.Replace(html.UnescapeString(q.Question), "\"", "'", -1),
					out,
				)

				initialQuestion := fmt.Sprintf(`MSG {"data": "%s"}`, x)
				if err = sendMessage(ctx, c, initialQuestion); err != nil {
					panic(err)
				}

				time.Sleep(20 * time.Second)
				z := fmt.Sprintf(`MSG {"data": "The correct answer is: %s"}`, q.CorrectAnswer)
				if err = sendMessage(ctx, c, z); err != nil {
					panic(err)
				}
			}
		}
	}
}

func sendMessage(ctx context.Context, c *websocket.Conn, input string) error {
	fmt.Println(input)
	if err := c.Write(
		ctx,
		websocket.MessageText,
		[]byte(input),
	); err != nil {
		return err
	}
	return nil
}

func requestToken() error {
	// TODO: query param
	resp, err := http.Get("https://opentdb.com/api_token.php?command=request")
	if err != nil {
		return err
	}

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	tokenRes := struct {
		Token string `json:"token"`
	}{}
	if err = json.Unmarshal(body, &tokenRes); err != nil {
		return err
	}

	u, err := url.Parse(triviaURL)
	if err != nil {
		return err
	}

	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return err
	}

	q.Add("token", tokenRes.Token)
	u.RawQuery = q.Encode()
	triviaURL = u.String()
	return nil
}

func requestTriviaData(ctx context.Context, client *http.Client) (*result, error) {
	req, err := http.NewRequest(http.MethodGet, triviaURL, nil)
	if err != nil {
		return nil, err
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	body, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	responseData := response{}
	if err = json.Unmarshal(body, &responseData); err != nil {
		return nil, err
	}

	return &responseData.Results[rand.Intn(len(responseData.Results))], nil
}
