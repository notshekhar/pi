package bedrock

import (
	"encoding/base64"
	"net/url"
	"testing"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
)

func TestConvertMergesConsecutiveRoles(t *testing.T) {
	out, err := convertPrompt(provider.Prompt{
		provider.UserMessage{Content: []provider.UserPart{provider.TextPart{Text: "one"}}},
		provider.UserMessage{Content: []provider.UserPart{provider.TextPart{Text: "two"}}},
		provider.AssistantMessage{Content: []provider.AssistantPart{provider.TextPart{Text: "ok"}}},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Messages) != 2 {
		t.Fatalf("messages = %d, want 2 (consecutive users merged)", len(out.Messages))
	}
	if got := len(out.Messages[0].Content); got != 2 {
		t.Errorf("user blocks = %d, want 2", got)
	}
}

func TestConvertDropsEmptyAssistantText(t *testing.T) {
	out, err := convertPrompt(provider.Prompt{
		provider.AssistantMessage{Content: []provider.AssistantPart{
			provider.TextPart{Text: "   "},
			provider.ToolCallPart{ToolCallID: "c1", ToolName: "read", Input: map[string]any{}},
		}},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Messages[0].Content) != 1 || out.Messages[0].Content[0].ToolUse == nil {
		t.Fatalf("expected only the tool call, got %+v", out.Messages[0].Content)
	}
}

func TestConvertToolResultIsUserTurn(t *testing.T) {
	out, err := convertPrompt(provider.Prompt{
		provider.AssistantMessage{Content: []provider.AssistantPart{
			provider.ToolCallPart{ToolCallID: "c1", ToolName: "read", Input: map[string]any{"p": "a"}},
		}},
		provider.ToolMessage{Content: []provider.ToolPart{
			provider.ToolResultPart{ToolCallID: "c1", ToolName: "read", Output: provider.ToolOutputText{Value: "hi"}},
		}},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Messages) != 2 {
		t.Fatalf("messages = %d", len(out.Messages))
	}
	if out.Messages[1].Role != "user" {
		t.Errorf("tool result role = %q, want user", out.Messages[1].Role)
	}
	if out.Messages[1].Content[0].ToolResult == nil {
		t.Fatal("expected a toolResult block")
	}
}

func TestConvertMistralNormalizesToolIDs(t *testing.T) {
	out, err := convertPrompt(provider.Prompt{
		provider.AssistantMessage{Content: []provider.AssistantPart{
			provider.ToolCallPart{ToolCallID: "tooluse_bpe71yCfRu2b5i-nKGDr5g", ToolName: "read", Input: map[string]any{}},
		}},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	id := out.Messages[0].Content[0].ToolUse.ToolUseID
	if id != "toolusebp" {
		t.Errorf("id = %q, want first 9 alphanumerics toolusebp", id)
	}
}

func TestConvertReasoningNeedsSignature(t *testing.T) {
	out, err := convertPrompt(provider.Prompt{
		provider.AssistantMessage{Content: []provider.AssistantPart{
			provider.ReasoningPart{Text: "thoughts"},
			provider.TextPart{Text: "answer"},
		}},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Warnings) == 0 {
		t.Fatal("unsigned reasoning should warn")
	}
	if len(out.Messages[0].Content) != 1 || out.Messages[0].Content[0].Text != "answer" {
		t.Fatalf("unsigned reasoning should be dropped, got %+v", out.Messages[0].Content)
	}
}

func TestConvertReasoningReplaysSignature(t *testing.T) {
	out, err := convertPrompt(provider.Prompt{
		provider.AssistantMessage{Content: []provider.AssistantPart{
			provider.ReasoningPart{
				Text: "thoughts",
				ProviderOptions: provider.ProviderOptions{
					"bedrock": {"signature": "sig-1"},
				},
			},
		}},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	block := out.Messages[0].Content[0].ReasoningContent
	if block == nil || block.ReasoningText == nil || block.ReasoningText.Signature != "sig-1" {
		t.Fatalf("reasoning = %+v", out.Messages[0].Content[0])
	}
}

func TestConvertCachePointFromAmazonBedrockKey(t *testing.T) {
	out, err := convertPrompt(provider.Prompt{
		provider.SystemMessage{
			Content: "sys",
			ProviderOptions: provider.ProviderOptions{
				"amazonBedrock": {"cachePoint": true},
			},
		},
		provider.UserMessage{Content: []provider.UserPart{provider.TextPart{Text: "hi"}}},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.System) != 2 || out.System[1].CachePoint == nil {
		t.Fatalf("system = %+v, want a cache point", out.System)
	}
}

func TestConvertImageAndS3URL(t *testing.T) {
	png := []byte{0x89, 0x50, 0x4e, 0x47}
	s3, _ := url.Parse("s3://bucket/img.png")

	out, err := convertPrompt(provider.Prompt{
		provider.UserMessage{Content: []provider.UserPart{
			provider.FilePart{MediaType: "image/png", Data: provider.FileDataBytes{Data: png}},
			provider.FilePart{MediaType: "image/jpeg", Data: provider.FileDataURL{URL: s3}},
		}},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if out.Messages[0].Content[0].Image == nil || out.Messages[0].Content[0].Image.Format != "png" {
		t.Fatalf("first = %+v", out.Messages[0].Content[0])
	}
	if loc := out.Messages[0].Content[1].Image.Source.S3Location; loc == nil || loc.URI != "s3://bucket/img.png" {
		t.Fatalf("s3 source = %+v", out.Messages[0].Content[1].Image)
	}
}

func TestConvertBase64File(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte("hello"))
	out, err := convertPrompt(provider.Prompt{
		provider.UserMessage{Content: []provider.UserPart{
			provider.FilePart{MediaType: "text/plain", Filename: "note.txt", Data: provider.FileDataBytes{Base64: encoded}},
		}},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	doc := out.Messages[0].Content[0].Document
	if doc == nil || string(doc.Source.Bytes) != "hello" {
		t.Fatalf("document = %+v", doc)
	}
}

func TestSanitizeDocumentName(t *testing.T) {
	if got := sanitizeDocumentName("report.pdf"); got != "report-pdf" {
		t.Errorf("got %q", got)
	}
	if got := sanitizeDocumentName("???"); got != "---" && got != "document" {
		// all non-allowed chars become hyphens; if empty after, "document"
		if got == "" {
			t.Fatal("empty name")
		}
	}
}

func TestNormalizeToolCallID(t *testing.T) {
	if got := normalizeToolCallID("tooluse_abc-def", false); got != "tooluse_abc-def" {
		t.Errorf("passthrough = %q", got)
	}
	if got := normalizeToolCallID("tooluse_abc-def", true); got != "tooluseab" {
		t.Errorf("mistral = %q, want tooluseab", got)
	}
}
