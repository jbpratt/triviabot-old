package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io/ioutil"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"nhooyr.io/websocket"
)

const addr = "wss://chat2.strims.gg/ws"

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

type chatterAnswer struct {
	user   string
	order  int
	answer int
	dur    time.Duration
}

func main() {
	var current *result
	var correct int
	var start time.Time

	inProgress := false
	players := []chatterAnswer{}

	jwt := os.Getenv("STRIMS_TOKEN")
	if jwt == "" {
		log.Fatal(fmt.Errorf("no jwt provided"))
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := requestToken(); err != nil {
		log.Fatal(err)
	}

	c, _, err := websocket.Dial(ctx, addr,
		&websocket.DialOptions{
			HTTPHeader: http.Header{
				"Cookie": []string{fmt.Sprintf("jwt=%s", jwt)},
			},
		})
	if err != nil {
		log.Fatal(err)
	}
	defer c.Close(websocket.StatusInternalError, "connection closed")

	fmt.Printf("Connected to chat... (%s)\n", addr)
	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			log.Fatalf("failed to read from conn: %v", err)
		}

		msg := strings.SplitN(string(data), " ", 2)
		var content map[string]interface{}
		if msg[0] == "MSG" {
			start = time.Now()
			if err = json.Unmarshal([]byte(msg[1]), &content); err != nil {
				log.Fatalf("failed to unmarshal msg response: %v %v", msg, err)
			}
			chatMsg := content["data"].(string)
			if strings.HasPrefix(chatMsg, "!trivia") && !inProgress {
				// TODO check if msg contains category
				fmt.Println("Starting trivia round. Requesting data")
				inProgress = true
				current, err = requestTriviaData()
				if err != nil {
					log.Fatal(err)
				}
				answers := []string{current.CorrectAnswer}
				answers = append(answers, current.IncorrectAnswers...)

				rand.Seed(time.Now().UnixNano())
				rand.Shuffle(len(answers), func(i, j int) { answers[i], answers[j] = answers[j], answers[i] })

				var out string
				for i, ans := range answers {
					out += fmt.Sprintf("`%d` %s ", i+1, strings.ReplaceAll(html.UnescapeString(ans), "\"", "'"))
				}

				for i, a := range answers {
					if a == current.CorrectAnswer {
						correct = i + 1
					}
				}

				x := fmt.Sprintf(
					"%s Trivia time answer is in 20s, whisper me the number! (%s) Question: `%s`... Possible answers: %s",
					randomEmote(), current.Category, strings.Replace(html.UnescapeString(current.Question), "\"", "'", -1), out,
				)

				initialQuestion := fmt.Sprintf(`MSG {"data": "%s"}`, x)
				if err = sendMessage(ctx, c, initialQuestion); err != nil {
					log.Fatal(err)
				}
				go func() {
					fmt.Println("sleeping")
					time.Sleep(20 * time.Second)
					fmt.Println("Determining winner...")
					out := fmt.Sprintf(`MSG {"data": "The correct answer is: %d %s. `, correct, strings.Replace(html.UnescapeString(current.CorrectAnswer), "\"", "'", -1))
					plus := `No one answered correctly PepeLaugh"}`
					if len(players) > 0 {
						sort.Slice(players, func(i, j int) bool {
							return players[i].order < players[j].order
						})
						for _, ans := range players {
							if ans.answer == correct {
								// award points based on duration, player count, question difficulty
								plus = fmt.Sprintf(`%s won this round. They answered in %s"}`, ans.user, ans.dur.String())
								break
							}
						}
					}

					if err = sendMessage(ctx, c, out+plus); err != nil {
						log.Fatal(err)
					}
					inProgress = false
					players = nil
				}()
			}
		} else if msg[0] == "PRIVMSG" && inProgress {
			if err = json.Unmarshal([]byte(msg[1]), &content); err != nil {
				log.Fatalf("failed to unmarshal msg response: %v %v", msg, err)
			}
			present := false
			user := content["nick"].(string)
			// check if user has already answered
			for _, pl := range players {
				// if we find the user, alert them
				if user == pl.user {
					out := fmt.Sprintf(`PRIVMSG {"data": "You have already answered! MiyanoBird", "nick": %q}`, user)
					if err = sendMessage(ctx, c, out); err != nil {
						log.Fatal(err)
					}
					present = true
					break
				}
			}

			if !present {
				ans, err := strconv.ParseInt(content["data"].(string), 0, 32)
				if err != nil {
					out := fmt.Sprintf(`PRIVMSG {"data": "I could not determine your answer FeelsPepoMan", "nick": %q}`, user)
					if err = sendMessage(ctx, c, out); err != nil {
						log.Fatal(err)
					}
				} else {
					fmt.Printf("%s is playing with %d\n", user, ans)
					players = append(players, chatterAnswer{
						order:  len(players) + 1,
						answer: int(ans),
						user:   user,
						dur:    time.Since(start),
					})
				}
			}
		}
	}
}

func sendMessage(ctx context.Context, c *websocket.Conn, input string) error {
	fmt.Printf("Sending: %s\n", input)
	if err := c.Write(
		ctx,
		websocket.MessageText,
		[]byte(input),
	); err != nil {
		return fmt.Errorf("failed to send message: %v", err)
	}
	return nil
}

func requestToken() error {
	// TODO: query param
	body, err := get("https://opentdb.com/api_token.php?command=request")
	if err != nil {
		return fmt.Errorf("failed to make get request: %v", err)
	}

	tokenRes := struct {
		Token string `json:"token"`
	}{}
	if err = json.Unmarshal(body, &tokenRes); err != nil {
		return fmt.Errorf("failed to unmarshal token: %v", err)
	}

	triviaURL, err = encodeURL(triviaURL, "token", tokenRes.Token)
	if err != nil {
		return fmt.Errorf("failed to encode url: %v", err)
	}
	return nil
}

func requestTriviaData() (*result, error) {
	body, err := get(triviaURL)
	if err != nil {
		return nil, fmt.Errorf("failed to make get request: %v", err)
	}
	responseData := response{}
	if err = json.Unmarshal(body, &responseData); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response data: %v", err)
	}

	if err = filterQuestions(&responseData); err != nil {
		return nil, err
	}

	return &responseData.Results[rand.Intn(len(responseData.Results))], nil
}

func get(url string) ([]byte, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return body, nil
}

func filterQuestions(res *response) error {
	r, err := regexp.Compile("19[0-9]\\d{1}")
	if err != nil {
		return fmt.Errorf("failed to compile regexp: %v", err)
	}

	for i, trivia := range res.Results {
		if r.MatchString(trivia.Question) && trivia.Category == "Entertainment: Music" {
			res.Results[i] = res.Results[0]
			res.Results = res.Results[1:]
		}
	}
	return nil
}

func encodeURL(addr, key, value string) (string, error) {
	u, err := url.Parse(addr)
	if err != nil {
		return "", fmt.Errorf("failed to parse url: %v", err)
	}

	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return "", fmt.Errorf("failed to parse query: %v", err)
	}

	q.Add(key, value)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func randomEmote() string {
	emotes := []string{"POGGERS", "SOY", "PepoGood", "PepoG", "PepoHmm"}
	return emotes[rand.Intn(len(emotes))]
}
