// genicon 从方形 PNG 生成多尺寸 Windows 图标（.ico）。
//
// 产物 internal/server/webui/favicon.ico 有两个消费方：一是 Web UI（随 webui 目录
// 一起 embed，浏览器按 /favicon.ico 取用），二是 genwinres——它把同一个文件作为
// IconPath 编进 .syso，于是 wen.exe 在资源管理器里的图标与浏览器标签页图标同源。
// 只生成一份是有意的：两处分开维护迟早会漂移成两个不一样的图标。
//
// 与 genwinres 一样，本工具假定工作目录是 cmd/wen（由那里的 go:generate 指令驱动），
// 路径按此相对书写；两条指令的先后有依赖——先 genicon 出 .ico，genwinres 才能读到它。
// 只有换了 logo 图源后需要重新执行 go generate ./cmd/wen。
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/png"
	"log"
	"os"
)

const (
	srcPath = "../../internal/server/webui/assets/logo-512.png"
	outPath = "../../internal/server/webui/favicon.ico"
)

// sizes 是图标内含的边长。小尺寸用未压缩位图、128 起用 PNG 压缩：
// PNG 压缩条目要 Vista 以后才认，而小图标恰恰会出现在最老的那些界面里
// （任务栏、Alt+Tab、旧版对话框），位图形式在哪儿都能画出来。
var sizes = []int{16, 24, 32, 48, 64, 128, 256}

// pngFrom 是改用 PNG 压缩的起始边长。
const pngFrom = 128

func main() {
	src, err := loadPNG(srcPath)
	if err != nil {
		log.Fatalf("读取图源失败: %v", err)
	}
	b := src.Bounds()
	if b.Dx() != b.Dy() {
		log.Fatalf("图源不是正方形（%dx%d），缩放后会变形", b.Dx(), b.Dy())
	}
	for _, s := range sizes {
		if s > b.Dx() {
			log.Fatalf("图源边长 %d 小于要生成的 %d，放大只会糊掉", b.Dx(), s)
		}
	}

	var frames [][]byte
	for _, s := range sizes {
		img := resize(src, s)
		var data []byte
		if s >= pngFrom {
			data, err = encodePNG(img)
		} else {
			data, err = encodeDIB(img)
		}
		if err != nil {
			log.Fatalf("编码 %dx%d 失败: %v", s, s, err)
		}
		frames = append(frames, data)
	}

	ico := buildICO(sizes, frames)
	if err := os.WriteFile(outPath, ico, 0644); err != nil {
		log.Fatalf("写出 %s 失败: %v", outPath, err)
	}
	fmt.Printf("已生成 %s（%d 个尺寸，%d 字节）\n", outPath, len(sizes), len(ico))
}

func loadPNG(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return png.Decode(f)
}

// resize 用区域平均（box filter）把图缩到 size×size。只用于缩小，
// 此时区域平均等价于对源像素做完整重采样，不会像最近邻那样丢边缘。
// 颜色按预乘 alpha 平均，否则透明像素的颜色分量会把边缘拉暗。
func resize(src image.Image, size int) *image.NRGBA {
	b := src.Bounds()
	dst := image.NewNRGBA(image.Rect(0, 0, size, size))
	for y := range size {
		y0 := b.Min.Y + y*b.Dy()/size
		y1 := b.Min.Y + (y+1)*b.Dy()/size
		for x := range size {
			x0 := b.Min.X + x*b.Dx()/size
			x1 := b.Min.X + (x+1)*b.Dx()/size
			var sr, sg, sb, sa, n uint64
			for sy := y0; sy < y1; sy++ {
				for sx := x0; sx < x1; sx++ {
					// At().RGBA() 返回的就是预乘 alpha 的 16 位分量
					r, g, bl, a := src.At(sx, sy).RGBA()
					sr += uint64(r)
					sg += uint64(g)
					sb += uint64(bl)
					sa += uint64(a)
					n++
				}
			}
			if n == 0 {
				continue
			}
			ar, ag, ab, aa := sr/n, sg/n, sb/n, sa/n
			i := dst.PixOffset(x, y)
			if aa == 0 {
				dst.Pix[i], dst.Pix[i+1], dst.Pix[i+2], dst.Pix[i+3] = 0, 0, 0, 0
				continue
			}
			// 存回 NRGBA 需要反预乘
			dst.Pix[i] = uint8(ar * 0xffff / aa >> 8)
			dst.Pix[i+1] = uint8(ag * 0xffff / aa >> 8)
			dst.Pix[i+2] = uint8(ab * 0xffff / aa >> 8)
			dst.Pix[i+3] = uint8(aa >> 8)
		}
	}
	return dst
}

func encodePNG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// encodeDIB 输出 ICO 条目用的位图形式：一个高度写成两倍的 BITMAPINFOHEADER，
// 后接自下而上的 32 位 BGRA 像素，再接一张 AND 掩码。32 位色本身带 alpha，
// 掩码在这里是历史包袱，但少了它一些旧界面会画出黑底，所以按 alpha 填一份。
func encodeDIB(img *image.NRGBA) ([]byte, error) {
	w := img.Bounds().Dx()
	h := img.Bounds().Dy()

	var buf bytes.Buffer
	hdr := struct {
		Size          uint32
		Width         int32
		Height        int32
		Planes        uint16
		BitCount      uint16
		Compression   uint32
		SizeImage     uint32
		XPelsPerMeter int32
		YPelsPerMeter int32
		ClrUsed       uint32
		ClrImportant  uint32
	}{
		Size:      40,
		Width:     int32(w),
		Height:    int32(h * 2), // 约定：像素区高度 + 掩码区高度
		Planes:    1,
		BitCount:  32,
		SizeImage: uint32(w * h * 4),
	}
	if err := binary.Write(&buf, binary.LittleEndian, hdr); err != nil {
		return nil, err
	}

	// 像素区自下而上
	for y := h - 1; y >= 0; y-- {
		for x := range w {
			i := img.PixOffset(x, y)
			buf.Write([]byte{img.Pix[i+2], img.Pix[i+1], img.Pix[i], img.Pix[i+3]}) // BGRA
		}
	}

	// AND 掩码：每行按 32 位对齐，位为 1 表示该像素透明
	rowBytes := ((w + 31) / 32) * 4
	for y := h - 1; y >= 0; y-- {
		row := make([]byte, rowBytes)
		for x := range w {
			if img.Pix[img.PixOffset(x, y)+3] == 0 {
				row[x/8] |= 0x80 >> (x % 8)
			}
		}
		buf.Write(row)
	}
	return buf.Bytes(), nil
}

// buildICO 拼出 ICONDIR + 若干 ICONDIRENTRY + 各条目数据。
func buildICO(sizes []int, frames [][]byte) []byte {
	const dirSize, entrySize = 6, 16
	offset := dirSize + entrySize*len(frames)

	var head bytes.Buffer
	binary.Write(&head, binary.LittleEndian, uint16(0)) // 保留位
	binary.Write(&head, binary.LittleEndian, uint16(1)) // 类型：图标
	binary.Write(&head, binary.LittleEndian, uint16(len(frames)))

	var body bytes.Buffer
	for i, data := range frames {
		s := sizes[i]
		// 边长字段只有一个字节，256 约定写作 0
		dim := byte(s)
		if s >= 256 {
			dim = 0
		}
		head.Write([]byte{dim, dim, 0, 0}) // 宽、高、调色板色数、保留位
		binary.Write(&head, binary.LittleEndian, uint16(1))
		binary.Write(&head, binary.LittleEndian, uint16(32))
		binary.Write(&head, binary.LittleEndian, uint32(len(data)))
		binary.Write(&head, binary.LittleEndian, uint32(offset))
		offset += len(data)
		body.Write(data)
	}
	return append(head.Bytes(), body.Bytes()...)
}
