package main

import (
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
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/golang/freetype/truetype"
	"github.com/mattn/go-runewidth"
	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
	"github.com/nfnt/resize"
	"golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/math/fixed"
	_ "golang.org/x/image/webp"
)

const name = "makeitquote"

const version = "0.0.5"

var revision = "HEAD"

var (
	//go:embed background.png
	backBin []byte
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
	opt := truetype.Options{
		Size:              25,
		DPI:               0,
		Hinting:           0,
		GlyphCacheEntries: 0,
		SubPixelsX:        0,
		SubPixelsY:        0,
	}
	dst := image.NewRGBA(bounds)
	draw.Draw(dst, bounds, &image.Uniform{color.Black}, image.ZP, draw.Src)

	if picture != "" {
		resp, err := http.Get(picture)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()

		b, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			return "", err
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

	face := truetype.NewFace(ft, &opt)
	dr := &font.Drawer{
		Dst:  dst,
		Src:  image.White,
		Face: face,
		Dot:  fixed.Point26_6{},
	}
	size := dr.Face.Metrics().Ascent.Floor() + dr.Face.Metrics().Descent.Floor()

	var i int
	var line string
	var buf bytes.Buffer
	for i, line = range strings.Split(content, "\n") {
		buf.WriteString(runewidth.Wrap(line, 40) + "\n")
	}
	dr.Dot.Y = fixed.I(100)
	for i, line = range strings.Split(buf.String(), "\n") {
		dr.Dot.X = fixed.I(480)
		dr.Dot.Y = fixed.I(100 + i*size)
		for _, r := range line {
			fp := fmt.Sprintf("%s/emoji_u%.4x.png", filepath.Join(baseDir, "png"), r)
			_, err = os.Stat(fp)
			if err == nil {
				fp, err := os.Open(fp)
				if err != nil {
					fmt.Fprintln(os.Stderr, err)
					continue
				}
				emoji, _, err := image.Decode(fp)
				fp.Close()
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
			} else if r == 65038 {
				continue
			} else {
				dr.DrawString(string(r))
			}
		}
	}
	dr.Dot.X = (fixed.I(480))
	dr.Dot.Y = fixed.I(100 + (i+2)*30)
	dr.DrawString(name)

	buf.Reset()
	err = png.Encode(&buf, dst)
	if err != nil {
		return "", err
	}
	return upload(&buf)
}

var (
	rs = []string{
		"wss://relay-jp.nostr.wirednet.jp",
		"wss://relay.snort.social",
		"wss://relay.damus.io",
	}
	baseDir string
	fontFn  string
)

func init() {
	if dir, err := os.Executable(); err != nil {
		log.Fatal(err)
	} else {
		baseDir = filepath.Dir(dir)
	}
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
	ev.CreatedAt = time.Now()
	ev.Kind = nostr.KindTextNote
	ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"e", id, "", "reply"})
	ev.Sign(sk)
	success := 0
	for _, r := range rs {
		relay, err := nostr.RelayConnect(context.Background(), r)
		if err != nil {
			continue
		}
		status := relay.Publish(context.Background(), ev)
		relay.Close()
		if status != nostr.PublishStatusFailed {
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
		evs := relay.QuerySync(context.Background(), filter)
		relay.Close()
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
	flag.StringVar(&fontFn, "f", filepath.Join(baseDir, "Koruri-Regular.ttf"), "font filename")
	flag.Parse()
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

	dec := json.NewDecoder(io.TeeReader(os.Stdin, os.Stdout))
	for {
		var ev nostr.Event
		err := dec.Decode(&ev)
		if err != nil {
			break
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
