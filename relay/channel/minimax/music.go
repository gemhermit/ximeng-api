package minimax

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
)

type MiniMaxMusicRequest struct {
	Model             string        `json:"model"`
	Prompt            string        `json:"prompt,omitempty"`
	Lyrics            string        `json:"lyrics,omitempty"`
	Stream            bool          `json:"stream,omitempty"`
	AudioSetting      *AudioSetting `json:"audio_setting,omitempty"`
	OutputFormat      string        `json:"output_format,omitempty"`
	ReferVoice        string        `json:"refer_voice,omitempty"`
	ReferInstrumental string        `json:"refer_instrumental,omitempty"`
}

type MiniMaxMusicResponse struct {
	Data struct {
		Audio       string `json:"audio"`
		AudioURL    string `json:"audio_url"`
		AudioFile   string `json:"audio_file"`
		AudioBase64 string `json:"audio_base64"`
	} `json:"data"`
	ExtraInfo map[string]any  `json:"extra_info"`
	TraceID   string          `json:"trace_id"`
	BaseResp  MiniMaxBaseResp `json:"base_resp"`
}

func IsMiniMaxMusicModel(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	return strings.HasPrefix(model, "music-")
}

func audioRequest2MiniMaxMusicRequest(request dto.AudioRequest, info *relaycommon.RelayInfo) (MiniMaxMusicRequest, error) {
	outputFormat := strings.TrimSpace(request.ResponseFormat)
	if outputFormat == "" {
		outputFormat = "mp3"
	}

	musicRequest := MiniMaxMusicRequest{
		Model:        strings.TrimSpace(info.UpstreamModelName),
		Prompt:       request.Input,
		Lyrics:       request.Instructions,
		Stream:       request.StreamFormat == "sse",
		OutputFormat: outputFormat,
		AudioSetting: &AudioSetting{
			Format: outputFormat,
		},
	}
	if musicRequest.Model == "" {
		musicRequest.Model = strings.TrimSpace(info.OriginModelName)
	}
	if musicRequest.ReferVoice == "" {
		musicRequest.ReferVoice = strings.TrimSpace(request.Voice)
	}
	if len(request.Metadata) > 0 {
		if err := common.Unmarshal(request.Metadata, &musicRequest); err != nil {
			return MiniMaxMusicRequest{}, fmt.Errorf("error unmarshalling metadata to minimax music request: %w", err)
		}
	}
	if strings.TrimSpace(musicRequest.OutputFormat) == "" {
		musicRequest.OutputFormat = outputFormat
	}
	if musicRequest.AudioSetting == nil {
		musicRequest.AudioSetting = &AudioSetting{Format: musicRequest.OutputFormat}
	}
	if strings.TrimSpace(musicRequest.AudioSetting.Format) == "" {
		musicRequest.AudioSetting.Format = musicRequest.OutputFormat
	}
	return musicRequest, nil
}

func handleMusicResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (usage any, err *types.NewAPIError) {
	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return nil, types.NewErrorWithStatusCode(
			fmt.Errorf("failed to read minimax music response: %w", readErr),
			types.ErrorCodeReadResponseBodyFailed,
			http.StatusInternalServerError,
		)
	}
	defer resp.Body.Close()

	var minimaxResp MiniMaxMusicResponse
	if unmarshalErr := common.Unmarshal(body, &minimaxResp); unmarshalErr != nil {
		return nil, types.NewErrorWithStatusCode(
			fmt.Errorf("failed to unmarshal minimax music response: %w", unmarshalErr),
			types.ErrorCodeBadResponseBody,
			http.StatusInternalServerError,
		)
	}

	if minimaxResp.BaseResp.StatusCode != 0 {
		return nil, types.NewErrorWithStatusCode(
			fmt.Errorf("minimax music error: %d - %s", minimaxResp.BaseResp.StatusCode, minimaxResp.BaseResp.StatusMsg),
			types.ErrorCodeBadResponse,
			http.StatusBadRequest,
		)
	}

	audioPayload := firstNonEmpty(
		strings.TrimSpace(minimaxResp.Data.AudioURL),
		strings.TrimSpace(minimaxResp.Data.AudioFile),
		strings.TrimSpace(minimaxResp.Data.AudioBase64),
		strings.TrimSpace(minimaxResp.Data.Audio),
	)
	if audioPayload == "" {
		return nil, types.NewErrorWithStatusCode(
			fmt.Errorf("no audio data in minimax music response"),
			types.ErrorCodeBadResponse,
			http.StatusBadRequest,
		)
	}

	if strings.HasPrefix(audioPayload, "http://") || strings.HasPrefix(audioPayload, "https://") {
		c.Redirect(http.StatusFound, audioPayload)
	} else {
		audioData, decodeErr := decodeMiniMaxAudioPayload(audioPayload)
		if decodeErr != nil {
			return nil, types.NewErrorWithStatusCode(
				fmt.Errorf("failed to decode minimax music audio: %w", decodeErr),
				types.ErrorCodeBadResponseBody,
				http.StatusInternalServerError,
			)
		}
		format := strings.TrimSpace(c.GetString("response_format"))
		if format == "" && minimaxResp.Data.AudioBase64 != "" {
			format = "mp3"
		}
		c.Data(http.StatusOK, getContentTypeByFormat(format), audioData)
	}

	estimate := info.GetEstimatePromptTokens()
	if estimate <= 0 {
		estimate = 1
	}
	usage = &dto.Usage{
		PromptTokens:     estimate,
		CompletionTokens: 0,
		TotalTokens:      estimate,
	}
	return usage, nil
}

func decodeMiniMaxAudioPayload(payload string) ([]byte, error) {
	payload = strings.TrimSpace(payload)
	if payload == "" {
		return nil, fmt.Errorf("audio payload is empty")
	}

	if idx := strings.Index(payload, ","); idx >= 0 && strings.Contains(strings.ToLower(payload[:idx]), "base64") {
		payload = payload[idx+1:]
	}

	if decoded, err := hex.DecodeString(payload); err == nil {
		return decoded, nil
	}
	if decoded, err := base64.StdEncoding.DecodeString(payload); err == nil {
		return decoded, nil
	}
	if decoded, err := base64.RawStdEncoding.DecodeString(payload); err == nil {
		return decoded, nil
	}
	return nil, fmt.Errorf("unsupported audio payload encoding")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
