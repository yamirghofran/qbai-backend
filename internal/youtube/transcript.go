package youtube

import (
	"encoding/json"
	"fmt"
	"html"
	"io/ioutil"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

const (
	RE_YOUTUBE        = `(?:youtube\.com\/(?:[^\/]+\/.+\/|(?:v|e(?:mbed)?)\/|.*[?&]v=)|youtu\.be\/)([^"&?\/\s]{11})`
	RE_XML_TRANSCRIPT = `<text start="([^"]*)" dur="([^"]*)">([^<]*)<\/text>`
)

type TranscriptResponse struct {
	Text     string  `json:"text"`
	Duration float64 `json:"duration"`
	Offset   float64 `json:"offset"`
	Lang     string  `json:"lang"`
}

type YoutubeTranscript struct{}

func New() *YoutubeTranscript {
	return &YoutubeTranscript{}
}

func (yt *YoutubeTranscript) GetTranscript(url string, lang string) (string, error) {
	videoId, err := retrieveVideoId(url)
	if err != nil {
		return "", err
	}

	transcripts, _, err := yt.fetchTranscript(videoId, lang)
	if err != nil {
		return "", err
	}

	// Combine all transcript texts into one string
	var fullText strings.Builder
	for _, t := range transcripts {
		fullText.WriteString(html.UnescapeString(t.Text))
		fullText.WriteString(" ")
	}

	return fullText.String(), nil
}

func (yt *YoutubeTranscript) fetchTranscript(videoId string, lang string) ([]TranscriptResponse, string, error) {
	videoPageURL := fmt.Sprintf("https://www.youtube.com/watch?v=%s", videoId)
	videoPageResponse, err := http.Get(videoPageURL)
	if err != nil {
		return nil, "", fmt.Errorf("failed to fetch video page: %v", err)
	}
	defer videoPageResponse.Body.Close()

	videoPageBody, err := ioutil.ReadAll(videoPageResponse.Body)
	if err != nil {
		return nil, "", fmt.Errorf("failed to read video page body: %v", err)
	}

	// Extract video title
	titleRegex := regexp.MustCompile(`<title>(.+?) - YouTube</title>`)
	titleMatch := titleRegex.FindSubmatch(videoPageBody)
	var videoTitle string
	if len(titleMatch) > 1 {
		videoTitle = html.UnescapeString(string(titleMatch[1]))
	}

	splittedHTML := strings.Split(string(videoPageBody), `"captions":`)
	if len(splittedHTML) <= 1 {
		// Log the HTML around where captions were expected
		log.Printf("DEBUG: Could not find '\"captions\":' marker in video page HTML for video %s. HTML snippet near expected location: %s", videoId, getHTMLSnippet(string(videoPageBody), `"captions":`))
		return nil, "", fmt.Errorf("no captions available for video %s", videoId)
	}

	var captions struct {
		PlayerCaptionsTracklistRenderer struct {
			CaptionTracks []struct {
				BaseURL      string `json:"baseUrl"`
				LanguageCode string `json:"languageCode"`
			} `json:"captionTracks"`
		} `json:"playerCaptionsTracklistRenderer"`
	}

	captionsData := splittedHTML[1][:strings.Index(splittedHTML[1], ",\"videoDetails")]
	err = json.Unmarshal([]byte(captionsData), &captions)
	if err != nil {
		return nil, "", fmt.Errorf("failed to parse captions data: %v", err)
	}

	if len(captions.PlayerCaptionsTracklistRenderer.CaptionTracks) == 0 {
		// Log the parsed captions data if tracks are missing
		log.Printf("DEBUG: Parsed captions data for video %s, but CaptionTracks array is empty. Captions JSON: %s", videoId, captionsData)
		return nil, "", fmt.Errorf("no transcripts available for video %s", videoId)
	}

	var transcriptURL string
	if lang != "" {
		for _, track := range captions.PlayerCaptionsTracklistRenderer.CaptionTracks {
			if track.LanguageCode == lang {
				transcriptURL = track.BaseURL
				break
			}
		}
		if transcriptURL == "" {
			return nil, "", fmt.Errorf("no transcript available in language %s", lang)
		}
	} else {
		transcriptURL = captions.PlayerCaptionsTracklistRenderer.CaptionTracks[0].BaseURL
	}

	transcriptResponse, err := http.Get(transcriptURL)
	if err != nil {
		return nil, "", fmt.Errorf("failed to fetch transcript: %v", err)
	}
	defer transcriptResponse.Body.Close()

	transcriptBody, err := ioutil.ReadAll(transcriptResponse.Body)
	if err != nil {
		return nil, "", fmt.Errorf("failed to read transcript body: %v", err)
	}

	re := regexp.MustCompile(RE_XML_TRANSCRIPT)
	matches := re.FindAllStringSubmatch(string(transcriptBody), -1)
	var results []TranscriptResponse
	for _, match := range matches {
		duration, _ := strconv.ParseFloat(match[2], 64)
		offset, _ := strconv.ParseFloat(match[1], 64)
		results = append(results, TranscriptResponse{
			Text:     match[3],
			Duration: duration,
			Offset:   offset,
			Lang:     lang,
		})
	}

	return results, videoTitle, nil
}

func retrieveVideoId(url string) (string, error) {
	if len(url) == 11 {
		return url, nil
	}
	re := regexp.MustCompile(RE_YOUTUBE)
	match := re.FindStringSubmatch(url)
	if match != nil {
		return match[1], nil
	}
	return "", fmt.Errorf("invalid YouTube URL or video ID")
}

// Helper function to get a snippet of HTML around a search term
func getHTMLSnippet(htmlContent string, searchTerm string) string {
	index := strings.Index(htmlContent, searchTerm)
	start := 0
	if index > 200 {
		start = index - 200
	}
	end := len(htmlContent)
	if index != -1 && index+len(searchTerm)+200 < len(htmlContent) {
		end = index + len(searchTerm) + 200
	} else if index == -1 && 400 < len(htmlContent) {
		// If term not found, show beginning
		end = 400
	}

	snippet := htmlContent[start:end]
	if index == -1 {
		return fmt.Sprintf("[Search term '%s' not found] Start of HTML: %s...", searchTerm, snippet)
	}
	return fmt.Sprintf("...HTML around '%s': %s...", searchTerm, snippet)
}
