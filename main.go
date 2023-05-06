package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/color"
	_ "image/jpeg"
	"image/png"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/golang/freetype/truetype"
	"github.com/mattn/go-runewidth"
	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
	"github.com/nfnt/resize"
	"github.com/vincent-petithory/dataurl"
	"golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/math/fixed"

	_ "golang.org/x/image/webp"
	//_ "github.com/chai2010/webp"
	//_ "github.com/kolesa-team/go-webp/webp"
)

const name = "makeitquote"

const version = "0.0.19"

var revision = "HEAD"

var (
	rs = []string{
		"wss://relay-jp.nostr.wirednet.jp",
		"wss://nostr-relay.nokotaro.com/",
		"wss://nostr.h3z.jp/",
		"wss://nostr.wine/",
		"wss://relay.nostr.band",
		"wss://relay.snort.social",
		"wss://relay.damus.io",
		"wss://relay.nostrich.land/",
	}

	//go:embed background.png
	backBin []byte

	baseDir string
	pngDir  string
	fontFn  string
)

type Profile struct {
	Website     string `json:"website"`
	Nip05       string `json:"nip05"`
	Picture     string `json:"picture"`
	Lud16       string `json:"lud16"`
	DisplayName string `json:"display_name"`
	About       string `json:"about"`
	Name        string `json:"name"`
}

func upload(buf *bytes.Buffer) (string, error) {
	req, err := http.NewRequest(http.MethodPost, "https://void.cat/upload?cli=true", buf)
	if err != nil {
		return "", err
	}
	req.Header.Set("V-Content-Type", "image/png")
	result := sha256.Sum256(buf.Bytes())
	req.Header.Set("V-Full-Digest", hex.EncodeToString(result[:]))
	req.Header.Set("V-Filename", "image.png")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer req.Body.Close()

	b, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func drawString(dr *font.Drawer, dst *image.RGBA, size int, s string) int {
	x := dr.Dot.X.Round()
	y := dr.Dot.Y.Round()
	n := 0
	for i, line := range strings.Split(s, "\n") {
		dr.Dot.X = fixed.I(x)
		dr.Dot.Y = fixed.I(y + i*size)
		for _, r := range line {
			if r == 0xfe0e || r == 0xfe0f {
				continue
			}
			if !unicode.IsSymbol(r) {
				dr.DrawString(string(r))
			} else {
				fp := fmt.Sprintf("%s/emoji_u%.4x.png", pngDir, r)
				b, err := ioutil.ReadFile(fp)
				if err == nil {
					emoji, _, err := image.Decode(bytes.NewReader(b))
					if err != nil {
						fmt.Fprintln(os.Stderr, err)
						continue
					}
					rect := image.Rect(0, 0, size, size)
					cnv := image.NewRGBA(rect)
					draw.ApproxBiLinear.Scale(cnv, rect, emoji, emoji.Bounds(), draw.Over, nil)
					p := image.Pt(dr.Dot.X.Floor(), dr.Dot.Y.Floor()-dr.Face.Metrics().Ascent.Floor())
					fore := image.NewGray16(rect)
					for x := 0; x < rect.Dx(); x++ {
						for y := 0; y < rect.Dy(); y++ {
							fore.Set(x, y, color.GrayModel.Convert(cnv.At(x, y)))
						}
					}
					draw.Draw(dst, rect.Add(p), fore, image.ZP, draw.Over)
					dr.Dot.X += fixed.I(size)
				} else {
					dr.DrawString(string(r))
				}
			}
		}
		n++
	}
	return n
}

func boxsize(s string) (int, int) {
	lines := strings.Split(s, "\n")
	max := 0
	for _, line := range lines {
		w := runewidth.StringWidth(line)
		if w > max {
			max = w
		}
	}
	return max, len(lines)
}

func boxize(content string) (string, int, int) {
	s := content
	var maxw, maxh int
	for {
		maxw, maxh = boxsize(s)
		if maxw <= maxh*2+4 {
			break
		}
		var buf bytes.Buffer
		lines := strings.Split(content, "\n")
		for _, line := range lines {
			buf.WriteString(runewidth.Wrap(line, maxw-1) + "\n")
		}
		s = buf.String()
	}
	return s, maxw, maxh
}

func makeImage(name, content, picture string) (string, error) {
	back, _, err := image.Decode(bytes.NewReader(backBin))
	if err != nil {
		return "", err
	}
	bounds := back.Bounds()

	b, err := ioutil.ReadFile(fontFn)
	if err != nil {
		return "", err
	}
	ft, err := truetype.Parse(b)
	if err != nil {
		return "", err
	}
	dst := image.NewRGBA(bounds)
	draw.Draw(dst, bounds, &image.Uniform{color.Black}, image.ZP, draw.Src)

	if picture != "" {
		var b []byte
		if strings.HasPrefix(picture, "data:image/") {
			dataURL, err := dataurl.DecodeString(picture)
			if err != nil {
				return "", err
			}
			b = dataURL.Data
		} else {
			resp, err := http.Get(picture)
			if err != nil {
				return "", err
			}
			defer resp.Body.Close()

			b, err = ioutil.ReadAll(resp.Body)
			if err != nil {
				return "", err
			}
		}
		img, _, err := image.Decode(bytes.NewReader(b))
		if err != nil {
			return "", err
		}
		img = resize.Resize(0, uint(bounds.Dy()), img, resize.Lanczos3)

		mask := image.NewAlpha(bounds)
		for x := 0; x < bounds.Dx(); x++ {
			for y := 0; y < bounds.Dy(); y++ {
				_, _, _, a := back.At(x, y).RGBA()
				mask.SetAlpha(x, y, color.Alpha{uint8(255 - a)})
			}
		}
		fore := image.NewGray16(img.Bounds())
		for x := 0; x < bounds.Dx(); x++ {
			for y := 0; y < bounds.Dy(); y++ {
				fore.Set(x, y, color.GrayModel.Convert(img.At(x, y)))
			}
		}
		draw.DrawMask(dst, dst.Bounds(), fore, image.ZP, mask, image.ZP, draw.Over)
	}

	opt := truetype.Options{
		Size:              25,
		DPI:               0,
		Hinting:           0,
		GlyphCacheEntries: 0,
		SubPixelsX:        0,
		SubPixelsY:        0,
	}

	var buf bytes.Buffer
	sline, _, maxh := boxize(content)
	if maxh > 13 {
		opt.Size *= 13.0 / float64(maxh)
	}

	face := truetype.NewFace(ft, &opt)
	dr := &font.Drawer{
		Dst:  dst,
		Src:  image.White,
		Face: face,
		Dot:  fixed.Point26_6{},
	}
	size := dr.Face.Metrics().Ascent.Floor() + dr.Face.Metrics().Descent.Floor()
	dr.Dot.X = fixed.I(520)
	dr.Dot.Y = fixed.I(100)
	drawString(dr, dst, size, sline)

	opt = truetype.Options{
		Size:              25,
		DPI:               0,
		Hinting:           0,
		GlyphCacheEntries: 0,
		SubPixelsX:        0,
		SubPixelsY:        0,
	}
	face = truetype.NewFace(ft, &opt)
	dr = &font.Drawer{
		Dst:  dst,
		Src:  image.White,
		Face: face,
		Dot:  fixed.Point26_6{},
	}
	size = dr.Face.Metrics().Ascent.Floor() + dr.Face.Metrics().Descent.Floor()

	dr.Dot.X = fixed.I(480)
	dr.Dot.Y = fixed.I(dr.Dst.Bounds().Dy() - 30 - size)
	drawString(dr, dst, size, name)

	dr.Dot.X = fixed.I(600)
	dr.Dot.Y = fixed.I(dr.Dst.Bounds().Dy() - 30)
	dr.DrawString(time.Now().Format("2006/01/02 15:04:05 JST"))

	buf.Reset()
	err = png.Encode(&buf, dst)
	if err != nil {
		return "", err
	}
	return upload(&buf)
}

func init() {
	if dir, err := os.Executable(); err != nil {
		log.Fatal(err)
	} else {
		baseDir = filepath.Dir(dir)
	}
	time.Local = time.FixedZone("Local", 9*60*60)

}

func postEvent(nsec string, rs []string, id, content string) error {
	ev := nostr.Event{}

	var sk string
	if _, s, err := nip19.Decode(nsec); err != nil {
		return err
	} else {
		sk = s.(string)
	}
	if pub, err := nostr.GetPublicKey(sk); err == nil {
		if _, err := nip19.EncodePublicKey(pub); err != nil {
			return err
		}
		ev.PubKey = pub
	} else {
		return err
	}
	ev.Content = content
	ev.CreatedAt = nostr.Now()
	ev.Kind = nostr.KindTextNote
	ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"e", id, "", "reply"})
	ev.Sign(sk)
	success := 0
	for _, r := range rs {
		relay, err := nostr.RelayConnect(context.Background(), r)
		if err != nil {
			continue
		}
		status, err := relay.Publish(context.Background(), ev)
		relay.Close()
		if err == nil && status != nostr.PublishStatusFailed {
			success++
		}
	}
	if success == 0 {
		return errors.New("failed to publish")
	}
	return nil
}

func findEvents(rs []string, filter nostr.Filter) []*nostr.Event {
	for _, r := range rs {
		relay, err := nostr.RelayConnect(context.Background(), r)
		if err != nil {
			continue
		}
		evs, err := relay.QuerySync(context.Background(), filter)
		relay.Close()
		if err != nil {
			continue
		}
		if len(evs) > 0 {
			return evs
		}
	}
	return nil
}

func generate(rs []string, id string) (string, error) {
	evs := findEvents(rs, nostr.Filter{
		Kinds: []int{nostr.KindTextNote},
		IDs:   []string{id},
		Limit: 1,
	})
	if len(evs) == 0 {
		return "", errors.New("cannot find quoted note")
	}
	content := evs[0].Content

	evs = findEvents(rs, nostr.Filter{
		Kinds:   []int{nostr.KindSetMetadata},
		Authors: []string{evs[0].PubKey},
	})
	if len(evs) == 0 {
		return "", errors.New("cannot find author")
	}
	var profile Profile
	err := json.NewDecoder(strings.NewReader(evs[0].Content)).Decode(&profile)
	if err != nil {
		return "", err
	}
	name := profile.DisplayName + " (" + profile.Name + ")"
	img, err := makeImage(name, content, profile.Picture)
	if err != nil {
		return "", err
	}
	return img + ".png", nil
}

func main() {
	var showVersion bool
	flag.StringVar(&pngDir, "d", filepath.Join(baseDir, "png"), "png directory")
	flag.StringVar(&fontFn, "f", filepath.Join(baseDir, "Koruri-Regular.ttf"), "font filename")
	flag.BoolVar(&showVersion, "v", false, "show version")
	flag.Parse()

	if showVersion {
		fmt.Println(version)
		os.Exit(0)
	}

	if flag.NArg() > 0 {
		var id string
		if _, tmp, err := nip19.Decode(flag.Arg(0)); err != nil {
			log.Fatal(err)
		} else {
			id = tmp.(string)
		}
		img, err := generate(rs, id)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(id, img)
		return
	}

	nsec := os.Getenv("MAKEITQUOTE_NSEC")
	if nsec == "" {
		log.Fatal("MAKEITQUOTE_NSEC is not set")
	}

	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		text := scanner.Text()
		var ev nostr.Event
		err := json.Unmarshal([]byte(text), &ev)
		if err != nil {
			continue
		}
		if !strings.Contains(ev.Content, "#makeitquote") {
			continue
		}

		evs := findEvents(rs, nostr.Filter{
			Kinds: []int{nostr.KindTextNote},
			IDs:   []string{ev.ID},
			Limit: 1,
		})
		if len(evs) == 0 {
			log.Println("the event not fuond")
			continue
		}
		p := evs[0].Tags.GetLast([]string{"e"})
		if p == nil {
			log.Println("parent event not fuond")
			continue
		}
		img, err := generate(rs, p.Value())
		if err != nil {
			log.Println(err)
			continue
		}
		err = postEvent(nsec, rs, ev.ID, img)
		if err != nil {
			log.Println(err)
		}
	}
}
