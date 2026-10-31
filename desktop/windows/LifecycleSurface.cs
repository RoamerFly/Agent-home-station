using System;
using System.Diagnostics;
using System.Drawing;
using System.Drawing.Drawing2D;
using System.Runtime.InteropServices;
using System.Windows.Forms;

namespace WechatAgent.Desktop {
    // Animate this small buffered surface only; never animate Form.Opacity or
    // recreate the native host window to produce an entrance/exit effect.
    internal sealed class LifecycleSurface : Control {
        readonly Timer frames = new Timer { Interval=16 };
        readonly Stopwatch clock = new Stopwatch();
        string heading="正在启动", detail="正在准备本机服务…";
        bool running=true, completed;
        [DllImport("user32.dll")] static extern bool SystemParametersInfo(uint action,uint parameter,out bool enabled,uint flags);
        readonly bool motion;
        internal bool Animations { get { return motion; } }
        internal LifecycleSurface() {
            DoubleBuffered=true; BackColor=Color.FromArgb(11,16,24);
            bool enabled;motion=!SystemParametersInfo(0x1042,0,out enabled,0)||enabled;
            // Repaint only the small progress ring. The full WebView-sized
            // background and text do not need to be redrawn at animation rate.
            frames.Tick+=delegate {
                float scale=Math.Min(DeviceDpi/96f,Math.Min(Width/160f,Height/200f));
                int x=Width/2,y=(int)((Height-190*scale)/2+32*scale),r=(int)Math.Ceiling(34*scale);
                Invalidate(new Rectangle(x-r,y-r,2*r+1,2*r+1));
            };
            VisibleChanged+=delegate { if(Visible&&running&&motion){clock.Start();frames.Start();}else frames.Stop(); };
        }
        internal void State(string title,string message,bool active=true,bool done=false) {
            heading=title;detail=message;running=active;completed=done;
            if(Visible&&running&&motion){clock.Start();frames.Start();}else frames.Stop();
            Invalidate();
        }
        internal void Detail(string message) { detail=message;Invalidate(); }
        protected override void OnPaint(PaintEventArgs e) {
            base.OnPaint(e);var g=e.Graphics;g.SmoothingMode=SmoothingMode.AntiAlias;
            // Reserve room for the full ring, its rounded stroke and both text
            // lines at every DPI; never place the centre relative to the bottom.
            float dpi=g.DpiX/96f;
            float scale=Math.Min(dpi,Math.Min(Width/160f,Height/200f));
            int x=Width/2,y=(int)((Height-190*scale)/2+32*scale);
            float radius=28*scale;
            using(var track=new Pen(Color.FromArgb(41,55,77),4*scale))g.DrawEllipse(track,x-radius,y-radius,2*radius,2*radius);
            using(var stroke=new Pen(completed?Color.FromArgb(24,132,91):Color.FromArgb(35,105,215),4*scale)) {
                stroke.StartCap=stroke.EndCap=LineCap.Round;
                if(completed)g.DrawLines(stroke,new[]{new PointF(x-11*scale,y),new PointF(x-3*scale,y+8*scale),new PointF(x+13*scale,y-10*scale)});
                else g.DrawArc(stroke,x-radius,y-radius,2*radius,2*radius,motion?(float)(clock.Elapsed.TotalMilliseconds*.24%360):-90,running?100:300);
            }
            using(var title=new Font(Font.FontFamily,17*scale/dpi,FontStyle.Bold))
                TextRenderer.DrawText(g,heading,title,new Rectangle(12,y+(int)(44*scale),Math.Max(0,Width-24),(int)(36*scale)),Color.FromArgb(231,237,247),TextFormatFlags.HorizontalCenter|TextFormatFlags.EndEllipsis);
            using(var body=new Font(Font.FontFamily,Font.Size*scale/dpi))
                TextRenderer.DrawText(g,detail,body,new Rectangle(24,y+(int)(86*scale),Math.Max(0,Width-48),Math.Max(1,Height-y-(int)(86*scale))),Color.FromArgb(156,172,195),TextFormatFlags.HorizontalCenter|TextFormatFlags.WordBreak);
        }
        protected override void Dispose(bool disposing) { if(disposing)frames.Dispose();base.Dispose(disposing); }
    }
}
