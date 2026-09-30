package v2

import (
	"minimax-h3-tc/internal/config"
	"minimax-h3-tc/internal/domain"
	"minimax-h3-tc/internal/protocol"
)

type CreateRequest = domain.GenerationRequest
type ContentItem = domain.GenerationContent
type URLValue = domain.MediaURL
type ValidatedRequest = protocol.ValidatedRequest

const (
	MaxDecodedImageBytes = protocol.MaxDecodedImageBytes
	MaxDecodedVideoBytes = protocol.MaxDecodedVideoBytes
	MaxDecodedAudioBytes = protocol.MaxDecodedAudioBytes
)

func ValidateCreate(r CreateRequest, p map[string]config.GenerationProfile) (ValidatedRequest, error) {
	return protocol.ValidateCreate(r, p)
}
func ParseAudioDataURI(value string) (string, []byte, bool, error) {
	return protocol.ParseAudioDataURI(value)
}
