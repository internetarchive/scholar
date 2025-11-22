package harvesting

import (
	"bytes"
	_ "embed"
	"fmt"
	"reflect"
	"testing"
)

//go:embed crossref_sample.ndjson
var xrefSample []byte

func Test_chunk(t *testing.T) {
	r := bytes.NewReader(xrefSample)

	sequence := []struct {
		offset            int64
		expectedOffsets   [][]int64
		expectedBytesRead int64
		expectedEOF       bool
	}{
		{
			offset: 0,
			expectedOffsets: [][]int64{
				{0, 39429}, {39429, 5899}, {45328, 6600}, {51928, 4551},
				{56479, 1644}, {58123, 1758}, {59881, 4552}, {64433, 3544},
				{67977, 981}, {68958, 3036}},
			expectedBytesRead: 71994,
		},
		{
			offset: 71994,
			expectedOffsets: [][]int64{
				{71994, 1021}, {73015, 1718}, {74733, 1560}, {76293, 9180},
				{85473, 10279}, {95752, 10434}, {106186, 4353}, {110539, 3797},
				{114336, 13986}, {128322, 1588}},
			expectedBytesRead: 129910,
		},
		{
			offset: 129910,
			expectedOffsets: [][]int64{
				{129910, 1193}, {131103, 2425}, {133528, 4109}, {137637, 3755},
				{141392, 7007}, {148399, 7063}, {155462, 4004}, {159466, 19505},
				{178971, 23387}, {202358, 21511},
			},
			expectedBytesRead: 223869,
		},
		{
			offset: 223869,
			expectedOffsets: [][]int64{
				{223869, 31774}, {255643, 3417}, {259060, 4014}, {263074, 1233},
				{264307, 14758}, {279065, 3958}, {283023, 24093}, {307116, 21360},
				{328476, 3517}, {331993, 9773}},
			expectedBytesRead: 341766,
		},
		{
			offset: 341766,
			expectedOffsets: [][]int64{
				{341766, 12272}, {354038, 2211}, {356249, 1291}, {357540, 16254},
				{373794, 3176}, {376970, 2967}, {379937, 1305}, {381242, 1349},
				{382591, 1396}, {383987, 1231}},
			expectedBytesRead: 385218,
			expectedEOF:       true,
		},
	}

	for ix, step := range sequence {
		t.Run(fmt.Sprintf("step %d", ix), func(t *testing.T) {
			cfg := chunkCfg{
				Offset:    step.offset,
				BatchSize: 10,
				ChunkSize: 64,
			}
			out, err := chunk(cfg, r)
			if err != nil {
				t.Errorf("error: %s", err.Error())
			}

			if len(out.Offsets) != cfg.BatchSize && !out.EOF {
				t.Errorf("expected %d offsets, got %d", cfg.BatchSize, len(out.Offsets))
			}

			if !reflect.DeepEqual(step.expectedOffsets, out.Offsets) {
				t.Errorf("expected offsets %v, got offsets %v", step.expectedOffsets, out.Offsets)
			}

			if step.expectedBytesRead != out.BytesRead {
				t.Errorf("expected %d bytes read, saw %d", step.expectedBytesRead, out.BytesRead)
			}

			if step.expectedEOF != out.EOF {
				t.Errorf("unexpected EOF")
			}
		})
	}
}
