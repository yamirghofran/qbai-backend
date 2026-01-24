package models

// Discord Embed Structures (based on documentation)
type DiscordEmbedFooter struct {
	Text    string `json:"text,omitempty"`
	IconURL string `json:"icon_url,omitempty"`
}

type DiscordEmbedImage struct {
	URL string `json:"url,omitempty"`
}

type DiscordEmbedThumbnail struct {
	URL string `json:"url,omitempty"`
}

type DiscordEmbedAuthor struct {
	Name    string `json:"name,omitempty"`
	URL     string `json:"url,omitempty"`
	IconURL string `json:"icon_url,omitempty"`
}

type DiscordEmbedField struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline,omitempty"`
}

type DiscordEmbed struct {
	Title       string                 `json:"title,omitempty"`
	Description string                 `json:"description,omitempty"`
	URL         string                 `json:"url,omitempty"`
	Timestamp   string                 `json:"timestamp,omitempty"` // ISO8601 timestamp
	Color       int                    `json:"color,omitempty"`     // Decimal color code
	Footer      *DiscordEmbedFooter    `json:"footer,omitempty"`
	Image       *DiscordEmbedImage     `json:"image,omitempty"`
	Thumbnail   *DiscordEmbedThumbnail `json:"thumbnail,omitempty"`
	Author      *DiscordEmbedAuthor    `json:"author,omitempty"`
	Fields      []DiscordEmbedField    `json:"fields,omitempty"`
}

// WebhookPayload is the structure Discord expects for webhook requests with embeds
type WebhookPayload struct {
	Username  string         `json:"username,omitempty"`   // Optional: Override webhook username
	AvatarURL string         `json:"avatar_url,omitempty"` // Optional: Override webhook avatar
	Content   string         `json:"content,omitempty"`    // Optional: Message content outside embed
	Embeds    []DiscordEmbed `json:"embeds"`
}
