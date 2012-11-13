package main

import (
	"code.google.com/p/ebml-go/webm"
	"code.google.com/p/glcv-go/canvas"
	"code.google.com/p/glcv-go/key"
	"code.google.com/p/portaudio-go/portaudio"
	"flag"
	gl "github.com/chsc/gogl/gl21"
	"log"
	"math"
	"os"
	"runtime"
	"time"
)

var (
	in         = flag.String("i", "", "Input file (required)")
	unsync     = flag.Bool("u", false, "Unsynchronized display")
	notc       = flag.Bool("t", false, "Ignore timecodes")
	blend      = flag.Bool("b", false, "Blend between images")
	fullscreen = flag.Bool("f", false, "Fullscreen mode")
	justaudio  = flag.Bool("a", false, "Just audio")
	justvideo  = flag.Bool("v", false, "Just video")
	loops      = flag.Int("l", 0, "Loops")
)

const (
	vss = `
void main() {
  gl_TexCoord[0] = gl_MultiTexCoord0;
  gl_Position = ftransform();
}
`

	ycbcr2rgb = `
const mat3 ycbcr2rgb = mat3(
                          1.164, 0, 1.596,
                          1.164, -0.392, -0.813,
                          1.164, 2.017, 0.0
                          );
const float ysub = 0.0625;
vec3 ycbcr2rgb(vec3 c) {
   vec3 ycbcr = vec3(c.x - ysub, c.y - 0.5, c.z - 0.5);
   return ycbcr * ycbcr2rgb;
}
`

	fss = ycbcr2rgb + `
uniform sampler2D yt1;
uniform sampler2D cbt1;
uniform sampler2D crt1;

void main() {
   vec3 c = vec3(texture2D(yt1, gl_TexCoord[0].st).r,
                 texture2D(cbt1, gl_TexCoord[0].st).r,
                 texture2D(crt1, gl_TexCoord[0].st).r);
   gl_FragColor = vec4(ycbcr2rgb(c), 1.0);
}
`
	bfss = ycbcr2rgb + `
uniform sampler2D yt1;
uniform sampler2D cbt1;
uniform sampler2D crt1;
uniform sampler2D yt0;
uniform sampler2D cbt0;
uniform sampler2D crt0;
uniform float factor;

void main() {
   vec3 c0 = vec3(texture2D(yt0, gl_TexCoord[0].st).r,
                  texture2D(cbt0, gl_TexCoord[0].st).r,
                  texture2D(crt0, gl_TexCoord[0].st).r);
   vec3 c1 = vec3(texture2D(yt1, gl_TexCoord[0].st).r,
                  texture2D(cbt1, gl_TexCoord[0].st).r,
                  texture2D(crt1, gl_TexCoord[0].st).r);
   gl_FragColor = vec4(ycbcr2rgb(mix(c0, c1, factor)), 1);
}
`
)

func texinit(id int) {
	gl.BindTexture(gl.TEXTURE_2D, gl.Uint(id))
	gl.TexParameterf(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.LINEAR)
	gl.TexParameterf(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.LINEAR)
	gl.TexParameterf(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.CLAMP_TO_EDGE)
	gl.TexParameterf(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE)
}

func shinit() gl.Int {
	vs := loadShader(gl.VERTEX_SHADER, vss)
	var sfs string
	if *blend {
		sfs = bfss
	} else {
		sfs = fss
	}
	fs := loadShader(gl.FRAGMENT_SHADER, sfs)
	prg := gl.CreateProgram()
	gl.AttachShader(prg, vs)
	gl.AttachShader(prg, fs)
	gl.LinkProgram(prg)
	var l int
	if *blend {
		l = 6
	} else {
		l = 3
	}
	gl.UseProgram(prg)
	names := []string{"yt1", "cbt1", "crt1", "yt0", "cbt0", "crt0"}
	for i := 0; i < l; i++ {
		loc := gl.GetUniformLocation(prg, gl.GLString(names[i]))
		gl.Uniform1i(loc, gl.Int(i))
	}
	return gl.GetUniformLocation(prg, gl.GLString("factor"))
}

func upload(id gl.Uint, data []byte, stride int, w int, h int) {
	gl.BindTexture(gl.TEXTURE_2D, id)
	gl.PixelStorei(gl.UNPACK_ROW_LENGTH, gl.Int(stride))
	gl.PixelStorei(gl.UNPACK_ALIGNMENT, 1)
	gl.TexImage2D(gl.TEXTURE_2D, 0, gl.LUMINANCE,
		gl.Sizei(w), gl.Sizei(h), 0,
		gl.LUMINANCE, gl.UNSIGNED_BYTE, gl.Pointer(&data[0]))
}

func initquad() {
	ver := []gl.Float{-1, 1, 1, 1, -1, -1, 1, -1}
	gl.BindBuffer(gl.ARRAY_BUFFER, 1)
	gl.BufferData(gl.ARRAY_BUFFER, gl.Sizeiptr(4*len(ver)),
		gl.Pointer(&ver[0]), gl.STATIC_DRAW)
	gl.VertexPointer(2, gl.FLOAT, 0, nil)
	tex := []gl.Float{0, 0, 1, 0, 0, 1, 1, 1}
	gl.BindBuffer(gl.ARRAY_BUFFER, 2)
	gl.BufferData(gl.ARRAY_BUFFER, gl.Sizeiptr(4*len(tex)),
		gl.Pointer(&tex[0]), gl.STATIC_DRAW)
	gl.TexCoordPointer(2, gl.FLOAT, 0, nil)
	gl.EnableClientState(gl.VERTEX_ARRAY)
	gl.EnableClientState(gl.TEXTURE_COORD_ARRAY)
}

func loadShader(shtype gl.Enum, src string) gl.Uint {
	sh := gl.CreateShader(shtype)
	gsrc := gl.GLString(src)
	gl.ShaderSource(sh, 1, &gsrc, nil)
	gl.CompileShader(sh)
	return sh
}

func factor(t time.Time, tc0 time.Time, tc1 time.Time) gl.Float {
	num := t.Sub(tc0)
	den := tc1.Sub(tc0)
	res := num.Seconds() / den.Seconds()
	res = math.Max(res, 0)
	res = math.Min(res, 1)
	return gl.Float(res)
}

type app struct {
	canvas.Canvas
	r         *os.File
	reader    *webm.Reader
	vchan     <-chan webm.Frame
	img       webm.Frame
	pimg      webm.Frame
	tbase     time.Time
	flushing  bool
	shutdown  bool
	seek      time.Duration
	duration  time.Duration
	fduration time.Duration
	steps     uint
	factorloc gl.Int
	pastream  *portaudio.Stream
	aw        *AudioWriter
}

func (a *app) tseek(t time.Duration) {
	a.seek = t
	if a.seek < 0 {
		a.seek = 0
	}
}

func (a *app) xseek(x int) {
	factor := float64(x) / float64(a.Width())
	a.tseek(time.Duration(float64(a.duration) * factor))
}

func (a *app) OnMotion(x, y int) {
	if a.Pressed(key.MOUSELEFT) {
		a.xseek(x)
	}
}

func (a *app) OnPress(k key.Id) {
	switch k {
	case key.MOUSELEFT:
		x, _ := a.Mouse()
		a.xseek(x)
	case key.P:
		a.steps = 0
	case key.R:
		a.steps = 0xffffffff
		a.tseek(a.img.Timecode)
	case key.F:
		a.steps = 1
		a.tseek(a.img.Timecode + a.fduration)
	case key.B:
		a.steps = 1
		a.tseek(a.img.Timecode - a.fduration)
	case key.S:
		a.steps = 1
	}
}

func (a *app) Name() string {
	return *in
}

func (a *app) Geometry() (x, y, w, h int) {
	if *fullscreen {
		return -1, -1, -1, -1
	}
	return 20, 20, a.img.Rect.Dx(), a.img.Rect.Dy()
}

func (a *app) Borders() bool {
	return !*fullscreen
}

func (a *app) OnInit() {
	var err error
	a.r, err = os.Open(*in)
	if err != nil {
		log.Fatalf("Unable to open file '%s': %s", *in, err)
	}
	var wm webm.WebM
	a.reader, err = webm.Parse(a.r, &wm)
	if err != nil {
		log.Fatal("Unable to parse file: ", err)
	}
	a.duration = wm.GetDuration()
	var vtrack *webm.TrackEntry
	if !*justaudio {
		vtrack = wm.FindFirstVideoTrack()
	}
	var vstream *webm.Stream
	if vtrack != nil {
		vstream = webm.NewStream(vtrack)
		a.fduration = vtrack.GetDefaultDuration()
		a.vchan = vstream.VideoChannel()
	}
	var atrack *webm.TrackEntry
	if !*justvideo {
		atrack = wm.FindFirstAudioTrack()
	}
	var astream *webm.Stream
	if atrack != nil {
		astream = webm.NewStream(atrack)
	}
	splitter := webm.NewSplitter(a.reader.Chan)
	splitter.Split(astream, vstream)

	a.steps = uint(0xffffffff)
	a.img = <-a.vchan
	a.pimg = a.img

	chk := func(err error) {
		if err != nil {
			panic(err)
		}
	}
	channels := int(atrack.Audio.Channels)
	a.aw = &AudioWriter{ch: astream.AudioChannel(), channels: channels, active: true}
	a.pastream, err = portaudio.OpenDefaultStream(0, channels,
		atrack.Audio.SamplingFrequency, 0, a.aw)
	chk(err)
	chk(a.pastream.Start())

}

func (a *app) OnTerm() {
	a.pastream.Stop()
	a.pastream.Close()
	a.r.Close()
}

func (a *app) OnGLInit() {
	gl.Init()
	var ntex int
	if *blend {
		ntex = 6
	} else {
		ntex = 3
	}
	for i := 0; i < ntex; i++ {
		texinit(i + 1)
	}
	a.factorloc = shinit()
	initquad()
	gl.Enable(gl.TEXTURE_2D)
	gl.Disable(gl.DEPTH_TEST)
	a.tbase = time.Now()
}

func (a *app) OnResize(w, h int) {
	oaspect := float64(a.img.Rect.Dx()) / float64(a.img.Rect.Dy())
	haspect := float64(w) / float64(h)
	vaspect := float64(h) / float64(w)
	var scx, scy float64
	if oaspect > haspect {
		scx = 1
		scy = haspect / oaspect
	} else {
		scx = vaspect * oaspect
		scy = 1
	}
	gl.Viewport(0, 0, gl.Sizei(w), gl.Sizei(h))
	gl.LoadIdentity()
	gl.Scaled(gl.Double(scx), gl.Double(scy), 1)
}

func (a *app) OnClose() {
	if !a.shutdown {
		a.reader.Shutdown()
		a.shutdown = true
	}
}

func (a *app) OnUpdate() {
	if a.seek != webm.BadTC {
		a.flushing = true
		a.aw.flushing = true
		a.reader.Seek(a.seek)
		a.seek = webm.BadTC
	}
	t := time.Now()
	if a.flushing || a.shutdown ||
		(a.steps > 0 && (*notc || t.After(a.tbase.Add(a.img.Timecode)))) {
		a.pimg = a.img
		nimg, ok := <-a.vchan
		if !ok {
			a.Quit()
			return
		}
		if nimg.EOS {
			if *loops != 0 {
				*loops--
				a.tseek(0) //reader.Seek(0)
			} else if !a.shutdown {
				a.reader.Shutdown()
				a.shutdown = true
			}
		}

		if false && nimg.Timecode == a.pimg.Timecode {
			log.Println("same timecode", a.img.Timecode)
		}
		if nimg.Rebase {
			a.tbase = time.Now().Add(-nimg.Timecode)
			a.flushing = false
		}
		if !a.flushing && !nimg.EOS {
			if a.steps > 0 {
				a.steps--
			}
			a.img = nimg
		}
	}
	a.draw(t)
}

func (a *app) draw(t time.Time) {
	gl.Clear(gl.COLOR_BUFFER_BIT)
	img := a.img
	w := img.Rect.Dx()
	h := img.Rect.Dy()
	gl.ActiveTexture(gl.TEXTURE0)
	upload(1, img.Y, img.YStride, w, h)
	gl.ActiveTexture(gl.TEXTURE1)
	upload(2, img.Cb, img.CStride, w/2, h/2)
	gl.ActiveTexture(gl.TEXTURE2)
	upload(3, img.Cr, img.CStride, w/2, h/2)
	if *blend {
		pimg := a.pimg
		gl.Uniform1f(a.factorloc, factor(t,
			a.tbase.Add(pimg.Timecode),
			a.tbase.Add(img.Timecode)))
		gl.ActiveTexture(gl.TEXTURE3)
		upload(4, pimg.Y, pimg.YStride, w, h)
		gl.ActiveTexture(gl.TEXTURE4)
		upload(5, pimg.Cb, pimg.CStride, w/2, h/2)
		gl.ActiveTexture(gl.TEXTURE5)
		upload(6, pimg.Cr, pimg.CStride, w/2, h/2)
	}
	gl.DrawArrays(gl.TRIANGLE_STRIP, 0, 4)
	runtime.GC()
}

type AudioWriter struct {
	ch       <-chan webm.Samples
	channels int
	active   bool
	sofar    int
	curr     webm.Samples
	flushing bool
}

func (aw *AudioWriter) ProcessAudio(in, out []float32) {
	for sent, lo := 0, len(out); sent < lo; {
		if aw.sofar == len(aw.curr.Data) {
			var pkt webm.Samples
			pkt, aw.active = <-aw.ch
			if !aw.active {
				return
			}
			if pkt.Rebase {
				aw.flushing = false
			} else if pkt.EOS || aw.flushing {
				continue
			}
			aw.curr = pkt
			aw.sofar = 0
			//log.Println("timecode", aw.curr.Timecode)
		}
		s := copy(out[sent:], aw.curr.Data[aw.sofar:])
		sent += s
		aw.sofar += s
	}
}

func main() {
	flag.Parse()
	if *in == "" {
		flag.Usage()
		return
	}
	var a app
	a.InitCanvas(&a)
	a.Go()
}
