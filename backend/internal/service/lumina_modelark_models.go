package service

import (
	"sort"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/lumina"
)

// Lumina exposes its own internal service names (req_key like
// "high_aes_general_v40s"), while callers integrate through ModelArk-compatible
// SDKs that speak official model IDs like "seedream-4-0-250828". The table below
// is the bridge, so a synced account can be driven by the same model IDs an
// official ModelArk endpoint would accept.
//
// Official IDs are sourced from the ModelArk model list
// (https://docs.byteplus.com/en/docs/ModelArk/1330310, ap-southeast-1,
// retrieved 2026-07-28). req_keys and display names are sourced from a live
// capture of list_ai_services / get_schema_list on ai.byteplus.com the same day.
// Models Lumina offers but ModelArk does not host at all (GPT Image 2, Nano
// Banana, Seedream 3.0L, Seedance 1.0) get a normalized slug instead: an
// invented but stable public ID beats leaking an internal req_key into client
// configuration.
//
// Matching is primarily by catalog display name, because the display name tracks
// the product ("Seedream 4.5" is always seedream-4-5) while req_keys are internal
// identifiers that get revised. Names are compared through normalizeModelName,
// so case, spacing and full-width parentheses do not matter.
var luminaModelArkAliases = []luminaModelAlias{
	// Image services with an official ModelArk counterpart.
	{publicID: "dola-seedream-5-0-pro-260628", names: []string{"Seedream 5.0 Pro"}, reqKeys: []string{"ByteDance-Seedream-5.0-pro"}},
	{publicID: "seedream-5-0-260128", names: []string{"Seedream 5.0"}},
	{publicID: "seedream-5-0-lite-260128", names: []string{"Seedream 5.0 Lite"}, reqKeys: []string{"seedream_v50_ba"}},
	{publicID: "seedream-4-5-251128", names: []string{"Seedream 4.5"}, reqKeys: []string{"high_aes_general_v45_ba"}},
	{publicID: "seedream-4-0-250828", names: []string{"Seedream 4.0"}, reqKeys: []string{"high_aes_general_v40s"}},

	// Image services Lumina hosts on its own; slugs are ours.
	{publicID: "gpt-image-2", names: []string{"GPT Image 2", "gpt image2", "GPT Image 2 (Beta)"}, reqKeys: []string{"x2i_gpt_image2_lumina"}},
	{publicID: "nano-banana-2", names: []string{"Nano Banana 2", "nano banana 2", "Nano Banana 2 (Beta)"}, reqKeys: []string{"x2i_nano_lumina"}},
	{publicID: "nano-banana-pro", names: []string{"Nano Banana Pro", "nano banana pro", "Nano Banana Pro (Beta)"}, reqKeys: []string{"x2i_nano_pro_lumina"}},
	{publicID: "seedream-3-0l", names: []string{"Seedream 3.0L"}, reqKeys: []string{"high_aes_general_v30l"}},
	{publicID: "seedream-3-0l-art", names: []string{"Seedream 3.0L (Art Edition)"}, reqKeys: []string{"high_aes_general_v30l_art"}},

	// Video schemas with an official ModelArk counterpart. Lumina labels the
	// mainline Seedance 2.0 model "pro" in its req_key; ModelArk ships it
	// without that suffix.
	{publicID: "dreamina-seedance-2-0-260128", names: []string{"Seedance 2.0", "Seedance 2.0 Pro"}, reqKeys: []string{"Doubao-Seedance-2.0-pro"}},
	{publicID: "dreamina-seedance-2-0-fast-260128", names: []string{"Seedance 2.0 Fast"}, reqKeys: []string{"Doubao-Seedance-2.0-pro-fast"}},
	{publicID: "dreamina-seedance-2-0-mini-260615", names: []string{"Seedance 2.0 Mini"}, reqKeys: []string{"Doubao-Seedance-2.0-mini"}},
	{publicID: "seedance-1-5-pro-251215", names: []string{"Seedance 1.5 Pro"}, reqKeys: []string{"ByteDance-Seedance-1.5-pro"}},
	{publicID: "seedance-1-0-pro-250528", names: []string{"Seedance 1.0 Pro"}, reqKeys: []string{"ByteDance-Seedance-1.0-pro"}},
	{publicID: "seedance-1-0-pro-fast-251015", names: []string{"Seedance 1.0 Pro Fast"}, reqKeys: []string{"ByteDance-Seedance-1.0-pro-fast"}},

	// Video schemas Lumina hosts on its own.
	{publicID: "seedance-1-0", names: []string{"Seedance 1.0"}, reqKeys: []string{"seedance_i2v_pro_com"}},
}

type luminaModelAlias struct {
	publicID string
	names    []string
	reqKeys  []string
}

var (
	luminaAliasByName   = map[string]string{}
	luminaAliasByReqKey = map[string]string{}
)

func init() {
	for _, alias := range luminaModelArkAliases {
		for _, name := range alias.names {
			if key := normalizeModelName(name); key != "" {
				luminaAliasByName[key] = alias.publicID
			}
		}
		for _, reqKey := range alias.reqKeys {
			if key := normalizeModelName(reqKey); key != "" {
				luminaAliasByReqKey[key] = alias.publicID
			}
		}
	}
}

// UpstreamModelMapping pairs the model ID clients call with the identifier the
// account must forward upstream. They are equal whenever no translation applies.
type UpstreamModelMapping struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// luminaPublicModelID resolves the client-facing model ID for one catalog entry,
// falling back to the upstream identifier when the model is unknown to the table.
func luminaPublicModelID(upstreamID string, names ...string) string {
	for _, name := range names {
		if publicID, ok := luminaAliasByName[normalizeModelName(name)]; ok {
			return publicID
		}
	}
	if publicID, ok := luminaAliasByReqKey[normalizeModelName(upstreamID)]; ok {
		return publicID
	}
	return upstreamID
}

// luminaCatalogModelMappings converts a raw catalog into public-ID → req_key
// pairs, keeping only prompt-only capabilities. The second return value lists
// entries that were dropped and why, for diagnostics.
func luminaCatalogModelMappings(
	images []lumina.ImageService,
	buckets []lumina.VideoSchemaBucket,
) ([]UpstreamModelMapping, []string) {
	mappings := make([]UpstreamModelMapping, 0, len(images)+len(buckets)*4)
	skipped := make([]string, 0, len(images))

	for _, image := range images {
		upstreamID := luminaCatalogModelID(image.ReqKey, image.ID, image.NameEN, image.Name)
		if !image.SupportsTextToImage() {
			skipped = appendLuminaSkippedCandidate(skipped, upstreamID, strings.Join(image.InferenceTypes, "|"))
			continue
		}
		if upstreamID == "" {
			continue
		}
		mappings = append(mappings, UpstreamModelMapping{
			From: luminaPublicModelID(upstreamID, image.NameEN, image.Name),
			To:   upstreamID,
		})
	}

	for _, bucket := range buckets {
		for _, video := range bucket.Items {
			upstreamID := luminaCatalogModelID(video.ReqKey, video.ID, video.Name)
			if !video.IsTextToVideo() {
				skipped = appendLuminaSkippedCandidate(skipped, upstreamID, bucket.Type+"/"+video.InferenceType+"/"+video.TaskType)
				continue
			}
			if upstreamID == "" {
				continue
			}
			mappings = append(mappings, UpstreamModelMapping{
				From: luminaPublicModelID(upstreamID, video.Name),
				To:   upstreamID,
			})
		}
	}

	return dedupeLuminaModelMappings(mappings, skipped)
}

// dedupeLuminaModelMappings keeps one entry per public model ID. Duplicates would
// collide inside the account's model_mapping object, so the loser is reported as
// skipped rather than silently overwriting the winner.
func dedupeLuminaModelMappings(mappings []UpstreamModelMapping, skipped []string) ([]UpstreamModelMapping, []string) {
	sort.SliceStable(mappings, func(i, j int) bool {
		if mappings[i].From != mappings[j].From {
			return mappings[i].From < mappings[j].From
		}
		return mappings[i].To < mappings[j].To
	})
	deduped := make([]UpstreamModelMapping, 0, len(mappings))
	seen := make(map[string]string, len(mappings))
	for _, mapping := range mappings {
		if winner, exists := seen[mapping.From]; exists {
			if winner != mapping.To {
				skipped = appendLuminaSkippedCandidate(skipped, mapping.To, "duplicate of "+mapping.From)
			}
			continue
		}
		seen[mapping.From] = mapping.To
		deduped = append(deduped, mapping)
	}
	return deduped, skipped
}
