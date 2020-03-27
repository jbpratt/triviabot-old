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
	"regexp"
	"strconv"
	"strings"
	"time"

	"nhooyr.io/websocket"
)

const addr = "wss://chat.strims.gg/ws"

// TODO: query param
var (
	triviaURL = "https://opentdb.com/api.php?amount=10"
)

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
	var current *result
	var correct int
	inProgress := false
	timeup := false
	players := map[string]int{}

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
				current, err = requestTriviaData(ctx, &triviaClient)
				if err != nil {
					panic(err)
				}
				answers := []string{current.CorrectAnswer}
				answers = append(answers, current.IncorrectAnswers...)

				var out string
				for i, ans := range answers {
					out += fmt.Sprintf("`%d` %s ", i+1, strings.ReplaceAll(html.UnescapeString(ans), "\"", "'"))
				}

				for i, a := range answers {
					if a == current.CorrectAnswer {
						correct = i + 1
					}
				}

				rand.Seed(time.Now().UnixNano())
				rand.Shuffle(len(answers), func(i, j int) { answers[i], answers[j] = answers[j], answers[i] })
				x := fmt.Sprintf(
					"Trivia time! (%s) Question: `%s`... Possible answers: %s (answer in 20s, whisper me the number)",
					current.Category, strings.Replace(html.UnescapeString(current.Question), "\"", "'", -1),
					out,
				)

				initialQuestion := fmt.Sprintf(`MSG {"data": "%s"}`, x)
				if err = sendMessage(ctx, c, initialQuestion); err != nil {
					panic(err)
				}
				go func() {
					time.Sleep(20 * time.Second)
					inProgress = false
					timeup = true
				}()
			}
		} else if msg[0] == "PRIVMSG" && inProgress {
			if err = json.Unmarshal([]byte(msg[1]), &content); err != nil {
				panic(err)
			}
			user := content["nick"].(string)
			ans, err := strconv.ParseInt(content["data"].(string), 0, 32)
			if err != nil {
				out := fmt.Sprintf(`PRIVMSG {"data": "Could not determine answer", "nick": %q}`, user)
				if err = sendMessage(ctx, c, out); err != nil {
					panic(err)
				}
			} else {
				fmt.Printf("%s is playing with %d\n", user, ans)
				players[user] = int(ans)
			}
		}

		if timeup {
			fmt.Println("Determining winner...")
			out := fmt.Sprintf(`MSG {"data": "The correct answer is: %d %s.`, correct, current.CorrectAnswer)
			plus := ` No one answered correctly"}`
			if len(players) > 0 {
				for user, ans := range players {
					if ans == correct {
						plus := fmt.Sprintf(`%s won this round"}`, user)
						if err = sendMessage(ctx, c, out+plus); err != nil {
							panic(err)
						}
						break
					}
				}
				if err = sendMessage(ctx, c, out+plus); err != nil {
					panic(err)
				}
			} else {
				if err = sendMessage(ctx, c, out+plus); err != nil {
					panic(err)
				}
			}
			timeup = false
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

	r, err := regexp.Compile("19[0-9]\\d{1}")
	if err != nil {
		return nil, err
	}

	for i, trivia := range responseData.Results {
		if r.MatchString(trivia.Question) && trivia.Category == "Entertainment: Music" {
			responseData.Results[i] = responseData.Results[0]
			responseData.Results = responseData.Results[1:]
		}
	}

	return &responseData.Results[rand.Intn(len(responseData.Results))], nil
}
