package model

import (
	"github.com/slyrz/newscat/html"
	"github.com/slyrz/newscat/util"
)

const (
	chunkFeatureCap = 36
	boostFeatureCap = 10
)

// feature represents a feature vector.
type feature []float32

type chunkFeature [chunkFeatureCap]float32
type boostFeature [boostFeatureCap]float32

// featureWriter writes observations to feature vectors.
type featureWriter struct {
	feature feature
	Pos     int
}

func (fw *featureWriter) Assign(f feature) {
	// Panic if we haven't fully filled the current feature vector yet, but are
	// about to replace it with a new one. We don't want to produce initialized
	// feature vectors here.
	if fw.feature != nil && fw.Pos != cap(fw.feature) {
		panic("partially filled feature")
	}
	fw.feature = f
	fw.Pos = 0
}

// Write a value of type int, float32 or bool at given offset and skip the
// requested amount of components afterwards.
func (fw *featureWriter) write(val interface{}, off int, skip int) {
	comp := &fw.feature[fw.Pos+off]
	switch val := val.(type) {
	case int:
		*comp = float32(val)
	case float32:
		*comp = val
	case bool:
		if val {
			*comp = 1.0
		} else {
			*comp = 0.0
		}
	}
	if skip > 0 {
		fw.Skip(skip)
	}
}

// Write value at current position and move to the next.
func (fw *featureWriter) Write(val interface{}) {
	fw.write(val, 0, 1)
}

// Write value at offset, but don't move.
func (fw *featureWriter) WriteAt(val interface{}, off int) {
	fw.write(val, off, 0)
}

// Skip components.
func (fw *featureWriter) Skip(n int) {
	fw.Pos += n
}

type chunkFeatureWriter struct {
	featureWriter
}

// Entries with a "plus comment" indicate that the next N elements share
// the same offset intentionally.
var elementTypes = map[string]int{
	"p":   0,
	"a":   1,
	"div": 2,
	"h1":  3, // +5
	"h2":  3,
	"h3":  3,
	"h4":  3,
	"h5":  3,
	"h6":  3,
}

func (fw *chunkFeatureWriter) WriteElementType(chunk *html.Chunk) {
	// One hot encoding of the element type.
	fw.WriteAt(true, elementTypes[chunk.Base.Data])
	fw.Skip(4)
}

var parentTypes = map[string]int{
	"p":    0,
	"span": 1,
	"div":  2,
	"li":   3,
}

func (fw *chunkFeatureWriter) WriteParentType(chunk *html.Chunk) {
	// One hot encoding of the chunk's parent's element type.
	if chunk.Base.Parent != nil {
		fw.WriteAt(true, parentTypes[chunk.Base.Parent.Data])
	}
	fw.Skip(4)
}

func (fw *chunkFeatureWriter) WriteSiblingTypes(chunk *html.Chunk) {
	count := 0
	types := map[string]int{"a": 0, "p": 0, "img": 0}
	for _, siblingType := range chunk.GetSiblingTypes() {
		count += 1
		if val, ok := types[siblingType]; ok {
			types[siblingType] = val + 1
		}
	}
	fw.Write(count)
	fw.Write(types["a"])
	fw.Write(types["p"])
	fw.Write(types["img"])
	if count > 0 {
		fw.Write(float32(types["a"]) / float32(count))
		fw.Write(float32(types["p"]) / float32(count))
		fw.Write(float32(types["img"]) / float32(count))
	} else {
		fw.Skip(3)
	}
}

func (fw *chunkFeatureWriter) WriteAncestors(chunk *html.Chunk) {
	fw.Write((chunk.Ancestors & html.AncestorArticle) != 0)
	fw.Write((chunk.Ancestors & html.AncestorAside) != 0)
	fw.Write((chunk.Ancestors & html.AncestorBlockquote) != 0)
	fw.Write((chunk.Ancestors & html.AncestorList) != 0)
}

func (fw *chunkFeatureWriter) WriteTextStat(chunk *html.Chunk) {
	fw.Write(chunk.Text.Words)
	fw.Write(chunk.Text.Sentences)
	fw.Write(chunk.LinkText)
}

func (fw *chunkFeatureWriter) WriteTextStatSiblings(chunk *html.Chunk) {
	if chunk.Prev != nil {
		fw.Write(chunk.Prev.Block == chunk.Block)
		fw.Write(chunk.Prev.Text.Words)
		fw.Write(chunk.Prev.Text.Sentences)
	} else {
		fw.Skip(3)
	}
	if chunk.Next != nil {
		fw.Write(chunk.Next.Block == chunk.Block)
		fw.Write(chunk.Next.Text.Words)
		fw.Write(chunk.Next.Text.Sentences)
	} else {
		fw.Skip(3)
	}
}

func (fw *chunkFeatureWriter) WriteClassStat(chunk *html.Chunk, classes map[string]*html.TextStat) {
	var best *html.TextStat = nil
	for _, class := range chunk.Classes {
		if stat, ok := classes[class]; ok {
			if best == nil || (stat.Words/stat.Count) > (best.Words/best.Count) {
				best = stat
			}
		}
	}
	if best != nil {
		fw.Write(true)
		fw.Write(float32(best.Words) / float32(best.Count))
		fw.Write(float32(best.Sentences) / float32(best.Count))
	} else {
		fw.Write(false)
		fw.Skip(2)
	}
}

func (fw *chunkFeatureWriter) WriteClusterStat(chunk *html.Chunk, clusters map[*html.Chunk]*html.TextStat) {
	if stat, ok := clusters[chunk]; ok {
		fw.Write(stat.Words)
		fw.Write(stat.Sentences)
		fw.Write(stat.Count)
		fw.Write(float32(stat.Words) / float32(stat.Count))
		fw.Write(float32(stat.Sentences) / float32(stat.Count))
	} else {
		fw.Skip(5)
	}
}

type boostFeatureWriter struct {
	featureWriter
}

var (
	goodQualClass = util.NewRegexFromWords(
		"article",
		"catchline",
		"chapter",
		"content",
		"head",
		"intro",
		"introduction",
		"leadin",
		"main",
		"post",
		"story",
		"summary",
		"title",
	)
	poorQualClass = util.NewRegexFromWords(
		"author",
		"blog",
		"byline",
		"caption",
		"col",
		"comment",
		"description",
		"email",
		"excerpt",
		"image",
		"info",
		"menu",
		"metadata",
		"nav",
		"photo",
		"small",
		"teaser",
		"widget",
	)
)

func (fw *boostFeatureWriter) WriteChunk(chunk *html.Chunk) {
	goodQual := false
	poorQual := false
	for _, class := range chunk.Classes {
		goodQual = goodQual || goodQualClass.In(class)
		poorQual = poorQual || poorQualClass.In(class)
	}
	fw.Write(chunk.LinkText)
	fw.Write(chunk.Text.Words)
	fw.Write(chunk.Text.Sentences)
	fw.Write(goodQual)
	fw.Write(poorQual)
}

func (fw *boostFeatureWriter) WriteCluster(chunk *html.Chunk, cluster *cluster) {
	i := 0
	for ; i < len(cluster.Chunks); i++ {
		if cluster.Chunks[i] == chunk {
			break
		}
	}
	fw.Write(cluster.Score())
	fw.Write(cluster.Scores[i])
	if i > 0 {
		fw.Write(cluster.Scores[i-1])
	} else {
		fw.Write(-10)
	}
	if i < len(cluster.Chunks)-2 {
		fw.Write(cluster.Scores[i+1])
	} else {
		fw.Write(-10)
	}
}

func (fw *boostFeatureWriter) WriteTitleSimilarity(chunk *html.Chunk, title *util.Text) {
	switch chunk.Base.Data {
	case "h1", "h2", "h3":
		fw.Write(chunk.Text.Similarity(title))
	default:
		fw.Skip(1)
	}
}
