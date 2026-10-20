package segment

import (
	"image"
	"image/color"
	"sort"
)

type Config struct { Threshold, BackgroundTolerance, MinArea, MinWidth, MinHeight, Padding int }
type Region struct { Rect image.Rectangle; Area int }

// Find estimates the page background from border pixels, builds a foreground mask,
// and groups foreground pixels into components. It is intentionally conservative:
// photos touching each other may be merged and complex backgrounds can confuse it.
func Find(src image.Image, cfg Config) ([]Region, image.Image) {
	b := src.Bounds(); w, h := b.Dx(), b.Dy(); if w == 0 || h == 0 { return nil, image.NewGray(image.Rect(0,0,1,1)) }
	bg := estimateBackground(src)
	mask := make([]bool, w*h)
	for y:=0; y<h; y++ { for x:=0; x<w; x++ { c:=color.RGBAModel.Convert(src.At(b.Min.X+x,b.Min.Y+y)).(color.RGBA); if distance(c,bg) > cfg.Threshold+cfg.BackgroundTolerance/3 { mask[y*w+x] = true } } }
	// Close small gaps in borders using a light 3x3 dilation followed by erosion.
	mask = closeMask(mask,w,h)
	visited := make([]bool,len(mask)); regions := make([]Region,0)
	queue := make([]int,0,4096)
	for idx, on := range mask { if !on || visited[idx] { continue }; queue = queue[:0]; queue=append(queue,idx); visited[idx]=true; minX,minY,maxX,maxY:=idx%w,idx/w,idx%w,idx/w; area:=0
		for head:=0; head<len(queue); head++ { p:=queue[head]; x,y:=p%w,p/w; area++; if x<minX {minX=x}; if x>maxX {maxX=x}; if y<minY {minY=y}; if y>maxY {maxY=y}
			for dy:=-1;dy<=1;dy++ { for dx:=-1;dx<=1;dx++ { if dx==0&&dy==0 {continue}; nx,ny:=x+dx,y+dy; if nx<0||ny<0||nx>=w||ny>=h {continue}; ni:=ny*w+nx; if mask[ni]&&!visited[ni] {visited[ni]=true;queue=append(queue,ni)} } }
		}
		r:=image.Rect(minX,minY,maxX+1,maxY+1); if area<cfg.MinArea||r.Dx()<cfg.MinWidth||r.Dy()<cfg.MinHeight {continue}; if float64(r.Dx()*r.Dy()) > float64(w*h)*0.96 {continue}; r=r.Inset(0); pad:=cfg.Padding; r=image.Rect(max(0,r.Min.X-pad),max(0,r.Min.Y-pad),min(w,r.Max.X+pad),min(h,r.Max.Y+pad)); regions=append(regions,Region{Rect:r,Area:area})
	}
	sort.Slice(regions,func(i,j int)bool { if regions[i].Rect.Min.Y==regions[j].Rect.Min.Y {return regions[i].Rect.Min.X<regions[j].Rect.Min.X}; return regions[i].Rect.Min.Y<regions[j].Rect.Min.Y })
	out:=image.NewGray(image.Rect(0,0,w,h)); for i,v:=range mask {if v {out.SetGray(i%w,i/w,color.Gray{Y:255})}}
	return regions,out
}

func estimateBackground(img image.Image) color.RGBA { b:=img.Bounds(); var r,g,bl,n uint64; // Sample perimeter at intervals to avoid overweighting huge images.
	step:=max(1,max(b.Dx(),b.Dy())/500)
	for x:=b.Min.X;x<b.Max.X;x+=step { add:=func(p image.Point){c:=color.RGBAModel.Convert(img.At(p.X,p.Y)).(color.RGBA);r+=uint64(c.R);g+=uint64(c.G);bl+=uint64(c.B);n++}; add(image.Pt(x,b.Min.Y)); add(image.Pt(x,b.Max.Y-1)) }
	for y:=b.Min.Y;y<b.Max.Y;y+=step { for _,x:=range []int{b.Min.X,b.Max.X-1} {c:=color.RGBAModel.Convert(img.At(x,y)).(color.RGBA);r+=uint64(c.R);g+=uint64(c.G);bl+=uint64(c.B);n++} }
	if n==0{return color.RGBA{255,255,255,255}}; return color.RGBA{uint8(r/n),uint8(g/n),uint8(bl/n),255}
}
func distance(a,b color.RGBA) int { dr:=int(a.R)-int(b.R);dg:=int(a.G)-int(b.G);db:=int(a.B)-int(b.B); return (abs(dr)+abs(dg)+abs(db))/3 }
func closeMask(src []bool,w,h int) []bool { dil:=make([]bool,len(src)); out:=make([]bool,len(src)); for y:=0;y<h;y++ {for x:=0;x<w;x++ {on:=false;for dy:=-1;dy<=1&&!on;dy++ {for dx:=-1;dx<=1;dx++ {nx,ny:=x+dx,y+dy;if nx>=0&&ny>=0&&nx<w&&ny<h&&src[ny*w+nx]{on=true;break}}};dil[y*w+x]=on}};for y:=0;y<h;y++ {for x:=0;x<w;x++ {on:=true;for dy:=-1;dy<=1&&on;dy++ {for dx:=-1;dx<=1;dx++ {nx,ny:=x+dx,y+dy;if nx<0||ny<0||nx>=w||ny>=h||!dil[ny*w+nx]{on=false;break}}};out[y*w+x]=on}};return out }
func abs(x int)int{if x<0{return -x};return x}
func min(a,b int)int{if a<b{return a};return b};func max(a,b int)int{if a>b{return a};return b}
