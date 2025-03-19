package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
)

// GitHub webhook payload structures
type Repository struct {
	FullName string `json:"full_name"`
	HTMLURL  string `json:"html_url"`
}

type Sender struct {
	Login   string `json:"login"`
	HTMLURL string `json:"html_url"`
}

type PullRequest struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	HTMLURL string `json:"html_url"`
	Merged  bool   `json:"merged"`
	State   string `json:"state"`
}

type WorkflowRun struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	HTMLURL    string `json:"html_url"`
}

type GitHubEvent struct {
	Action      string      `json:"action"`
	Repository  Repository  `json:"repository"`
	Sender      Sender      `json:"sender"`
	PullRequest PullRequest `json:"pull_request"`
	WorkflowRun WorkflowRun `json:"workflow_run"`
}

// Discord message structures
type DiscordEmbed struct {
	Title       string              `json:"title"`
	Description string              `json:"description"`
	Color       int                 `json:"color"`
	URL         string              `json:"url,omitempty"`
	Fields      []DiscordEmbedField `json:"fields,omitempty"`
}

type DiscordEmbedField struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline,omitempty"`
}

type DiscordMessage struct {
	Content string         `json:"content,omitempty"`
	Embeds  []DiscordEmbed `json:"embeds,omitempty"`
}

// Channel webhook URLs
var (
	developmentChannelWebhook string
	testingChannelWebhook     string
)

func main() {
	// Load environment variables
	if err := godotenv.Load(); err != nil {
		log.Println("Warning: Error loading .env file")
	}

	// Get Discord webhook URLs from environment variables
	developmentChannelWebhook = os.Getenv("DISCORD_DEV_WEBHOOK_URL")
	testingChannelWebhook = os.Getenv("DISCORD_TEST_WEBHOOK_URL")

	if developmentChannelWebhook == "" || testingChannelWebhook == "" {
		log.Fatal("Discord webhook URLs not set in environment variables")
	}

	// Create Gin router
	router := gin.Default()

	// Add CORS middleware
	router.Use(func(c *gin.Context) {
		c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-GitHub-Event, X-Hub-Signature-256")
		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}
		c.Next()
	})

	// GitHub webhook endpoint
	router.POST("/webhook/github", handleGitHubWebhook)

	// Health check endpoint
	router.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{
			"status": "ok",
		})
	})

	// Start the server
	port := os.Getenv("PORT")
	if port == "" {
		port = "8088" // Default port
	}
	log.Printf("Starting webhook server on port %s", port)
	log.Fatal(router.Run(":" + port))
}

func handleGitHubWebhook(c *gin.Context) {
	// Get the event type from the header
	eventType := c.GetHeader("X-GitHub-Event")
	log.Printf("Received GitHub webhook event: %s", eventType)

	// Read the request body
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		log.Printf("Error reading request body: %v", err)
		c.JSON(400, gin.H{"error": "Unable to read request body"})
		return
	}

	// Parse the GitHub event
	var event GitHubEvent
	if err := json.Unmarshal(body, &event); err != nil {
		log.Printf("Error parsing webhook payload: %v", err)
		c.JSON(400, gin.H{"error": "Invalid JSON payload"})
		return
	}

	// Process different event types
	switch eventType {
	case "pull_request":
		handlePullRequestEvent(event)
	case "workflow_run":
		handleWorkflowRunEvent(event)
	default:
		log.Printf("Ignoring unhandled event type: %s", eventType)
	}

	// Respond to GitHub with a success message
	c.JSON(200, gin.H{"message": "Webhook received successfully"})
}

func handlePullRequestEvent(event GitHubEvent) {
	log.Printf("Processing pull request event: %s", event.Action)

	// We only want to handle specific actions
	actionsToProcess := map[string]bool{
		"opened":           true,
		"reopened":         true,
		"ready_for_review": true,
		"closed":           true,
	}

	if !actionsToProcess[event.Action] {
		log.Printf("Ignoring PR action: %s", event.Action)
		return
	}

	// If the PR is closed but not merged, we don't notify
	if event.Action == "closed" && !event.PullRequest.Merged {
		log.Printf("PR was closed without merging, not sending notification")
		return
	}

	// Determine the color based on the action
	color := 0x1D82F7 // Default blue color
	if event.Action == "closed" && event.PullRequest.Merged {
		color = 0x6E48CD // Purple for merged PRs
	}

	// Create a descriptive action message
	actionDesc := event.Action
	if event.Action == "closed" && event.PullRequest.Merged {
		actionDesc = "merged"
	}

	// Create the Discord message
	message := DiscordMessage{
		Embeds: []DiscordEmbed{
			{
				Title: fmt.Sprintf("Pull Request %s", actionDesc),
				Description: fmt.Sprintf("**%s** %s [#%d: %s](%s)",
					event.Sender.Login,
					actionDesc,
					event.PullRequest.Number,
					event.PullRequest.Title,
					event.PullRequest.HTMLURL),
				Color: color,
				URL:   event.PullRequest.HTMLURL,
				Fields: []DiscordEmbedField{
					{
						Name:   "Repository",
						Value:  fmt.Sprintf("[%s](%s)", event.Repository.FullName, event.Repository.HTMLURL),
						Inline: true,
					},
					{
						Name:   "PR Status",
						Value:  event.PullRequest.State,
						Inline: true,
					},
				},
			},
		},
	}

	// Send the message to the development channel
	sendDiscordMessage(developmentChannelWebhook, message)
}

func handleWorkflowRunEvent(event GitHubEvent) {
	log.Printf("Processing workflow run event: %s", event.Action)

	// Only process completed workflow runs
	if event.Action != "completed" {
		log.Printf("Ignoring workflow run action: %s", event.Action)
		return
	}

	// Determine color based on the conclusion
	color := 0xE6E6E6 // Gray for unknown status
	switch event.WorkflowRun.Conclusion {
	case "success":
		color = 0x2ECC71 // Green
	case "failure":
		color = 0xE74C3C // Red
	case "cancelled":
		color = 0xF39C12 // Yellow-Orange
	case "skipped":
		color = 0x95A5A6 // Gray-Blue
	}

	// Create the Discord message
	message := DiscordMessage{
		Embeds: []DiscordEmbed{
			{
				Title: fmt.Sprintf("Workflow Run %s", event.WorkflowRun.Conclusion),
				Description: fmt.Sprintf("Workflow **%s** %s",
					event.WorkflowRun.Name,
					event.WorkflowRun.Conclusion),
				Color: color,
				URL:   event.WorkflowRun.HTMLURL,
				Fields: []DiscordEmbedField{
					{
						Name:   "Repository",
						Value:  fmt.Sprintf("[%s](%s)", event.Repository.FullName, event.Repository.HTMLURL),
						Inline: true,
					},
					{
						Name:   "Triggered by",
						Value:  fmt.Sprintf("[%s](%s)", event.Sender.Login, event.Sender.HTMLURL),
						Inline: true,
					},
				},
			},
		},
	}

	// Send the message to the testing channel
	sendDiscordMessage(testingChannelWebhook, message)
}

func sendDiscordMessage(webhookURL string, message DiscordMessage) {
	// Convert message to JSON
	jsonData, err := json.Marshal(message)
	if err != nil {
		log.Printf("Error marshaling Discord message: %v", err)
		return
	}

	// Send HTTP POST to Discord webhook
	resp, err := http.Post(webhookURL, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		log.Printf("Error sending Discord message: %v", err)
		return
	}
	defer resp.Body.Close()

	// Check response status
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		log.Printf("Discord API error (status %d): %s", resp.StatusCode, string(bodyBytes))
		return
	}

	log.Printf("Discord message sent successfully")
}
