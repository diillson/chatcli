/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * vision_rejection.go — recovering a turn whose images the provider refused.
 *
 * An image goes out natively when the catalog, the model id, a learned
 * verdict or a probe says the model sees. When the provider still answers
 * "this model does not support image input" (a learned verdict gone stale, a
 * profile re-pointed to another model, CHATCLI_VISION_INPUT=native on a model
 * that turns out blind), the turn is not lost: the route is remembered as
 * blind, every image in the outgoing history is replaced by a description
 * from a vision model (the same describe-fallback the gate uses), and the
 * caller resends once. With no describer the images are dropped with the
 * same warning the gate prints, so the resend never carries them again.
 */
package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// describeRejectedImages handles a provider refusing image input for
// provider/model: it records the route as blind and rewrites the images of
// every given history (and of the session history) into text. It returns
// true when the caller should resend — err was an image rejection and at
// least one image was rewritten.
func (cli *ChatCLI) describeRejectedImages(ctx context.Context, err error, provider, model string, histories ...*[]models.Message) bool {
	if !isImageInputRejected(err) {
		return false
	}
	all := append([]*[]models.Message{&cli.history}, histories...)
	if !historiesCarryImages(all) {
		return false
	}
	cli.noteImagesRejected(err, provider, model)

	descriptions := map[string]string{} // image set → description ("" = none)
	warned := false
	for _, h := range all {
		if h == nil {
			continue
		}
		for i := range *h {
			msg := &(*h)[i]
			if len(msg.Images) == 0 {
				continue
			}
			key := imageSetKey(msg.Images)
			desc, done := descriptions[key]
			if !done {
				if d, ok := cli.describeImagesFallback(ctx, msg.Images); ok {
					desc = d
				}
				descriptions[key] = desc
			}
			if desc == "" && !warned {
				warned = true
				if !cli.unattended {
					fmt.Println(i18n.T("vision.warn.model_no_vision", model))
				}
			}
			msg.Content += desc
			msg.Images = nil
		}
	}
	return true
}

// describeImagesRejectedBy is describeRejectedImages for a caller that
// rebuilds its history from the attachments (the RPC turn): it returns the
// text to fold into the prompt in place of images and ok=true to resend.
// The text is empty, with the gate's warning, when no model can describe.
func (cli *ChatCLI) describeImagesRejectedBy(ctx context.Context, err error, provider, model string, images []models.ImageContent) (string, bool) {
	if len(images) == 0 || !isImageInputRejected(err) {
		return "", false
	}
	cli.noteImagesRejected(err, provider, model)
	desc, ok := cli.describeImagesFallback(ctx, images)
	if !ok {
		desc = ""
		if !cli.unattended {
			fmt.Println(i18n.T("vision.warn.model_no_vision", model))
		}
	}
	return desc, true
}

// noteImagesRejected remembers the route as blind.
func (cli *ChatCLI) noteImagesRejected(err error, provider, model string) {
	learnedVision.record(cli.stateRoot, provider, model, false, cli.logger)
	cli.logger.Warn("vision: the provider refused image input; describing the images and resending",
		zap.String("provider", provider), zap.String("model", model), zap.Error(err))
}

func historiesCarryImages(histories []*[]models.Message) bool {
	for _, h := range histories {
		if h == nil {
			continue
		}
		for _, m := range *h {
			if len(m.Images) > 0 {
				return true
			}
		}
	}
	return false
}

// imageSetKey identifies a message's images, so the same images shared by
// the session history and the outgoing copy are described once.
func imageSetKey(images []models.ImageContent) string {
	h := sha256.New()
	for _, img := range images {
		h.Write([]byte(img.MediaType))
		h.Write([]byte{0})
		h.Write(img.Data)
		h.Write([]byte(img.URL))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}
