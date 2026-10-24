(() => {
  'use strict';

  const reduceMotion = window.matchMedia?.('(prefers-reduced-motion: reduce)')?.matches || false;
  const mouse = { x: null, y: null };
  const states = new Map();
  const starColors = [[90,200,255],[180,225,255],[168,140,255],[255,255,255]];
  let raf = 0;
  let resizeTimer = 0;

  window.addEventListener('mousemove', event => { mouse.x = event.clientX; mouse.y = event.clientY; });
  window.addEventListener('mouseout', () => { mouse.x = null; mouse.y = null; });

  function randomBetween(min,max){ return Math.random()*(max-min)+min; }

  class ShootingStar {
    constructor(state){ this.state=state; this.reset(); }
    reset(){ const s=this.state; this.active=false; this.x=Math.random()*s.width; this.y=-100; this.vx=(Math.random()>.5?1:-1)*randomBetween(5,9); this.vy=randomBetween(5,9); this.length=randomBetween(50,100); this.opacity=randomBetween(.4,.8); this.timer=randomBetween(200,600); }
    update(){ const s=this.state; if(!this.active){ this.timer--; if(this.timer<=0&&!reduceMotion)this.active=true; return; } this.x+=this.vx; this.y+=this.vy; if(this.x<-200||this.x>s.width+200||this.y>s.height+200)this.reset(); }
    draw(ctx){ if(!this.active)return; const tailX=this.x-(this.vx*(this.length/10)); const tailY=this.y-(this.vy*(this.length/10)); const grad=ctx.createLinearGradient(this.x,this.y,tailX,tailY); grad.addColorStop(0,`rgba(255,255,255,${this.opacity})`); grad.addColorStop(1,'rgba(255,255,255,0)'); ctx.beginPath();ctx.moveTo(this.x,this.y);ctx.lineTo(tailX,tailY);ctx.strokeStyle=grad;ctx.lineWidth=1.5;ctx.stroke();ctx.beginPath();ctx.arc(this.x,this.y,1.5,0,Math.PI*2);ctx.fillStyle=`rgba(255,255,255,${Math.min(1,this.opacity+.2)})`;ctx.fill(); }
  }

  class Particle {
    constructor(state){ this.state=state; this.reset(true); }
    reset(initial=false){ const s=this.state; this.x=Math.random()*s.width; this.y=initial?Math.random()*s.height:-10; const motion=reduceMotion?0:1; this.baseVx=randomBetween(-.18,.18)*motion; this.baseVy=randomBetween(-.14,.22)*motion; this.vx=this.baseVx;this.vy=this.baseVy; const roll=Math.random(); if(roll>.94){this.radius=randomBetween(2.1,3.1);this.isBrightStar=true;}else if(roll>.68){this.radius=randomBetween(1.15,2);this.isBrightStar=false;}else{this.radius=randomBetween(.35,1.1);this.isBrightStar=false;} this.baseRadius=this.radius;this.baseAlpha=randomBetween(.2,.68);this.twinkleAmount=randomBetween(.04,.25);this.twinkleSpeed=randomBetween(.0007,.002);this.twinklePhase=Math.random()*Math.PI*2;this.color=starColors[Math.floor(Math.random()*starColors.length)]; }
    update(){ const s=this.state; this.vx=(this.vx*.96)+(this.baseVx*.04);this.vy=(this.vy*.96)+(this.baseVy*.04);this.x+=this.vx;this.y+=this.vy;if(this.radius>this.baseRadius)this.radius-=.015;if(this.x<-12)this.x=s.width+12;else if(this.x>s.width+12)this.x=-12;if(this.y<-12)this.y=s.height+12;else if(this.y>s.height+12)this.y=-12; }
    alpha(ts){ if(reduceMotion)return this.baseAlpha; const twinkle=Math.sin(ts*this.twinkleSpeed+this.twinklePhase)*this.twinkleAmount;return Math.max(.08,Math.min(.92,this.baseAlpha+twinkle)); }
    draw(ctx,ts){ const a=this.alpha(ts);const [r,g,b]=this.color;if(this.radius>1.2){const glow=ctx.createRadialGradient(this.x,this.y,0,this.x,this.y,this.radius*4);glow.addColorStop(0,`rgba(${r},${g},${b},${a*.45})`);glow.addColorStop(1,`rgba(${r},${g},${b},0)`);ctx.beginPath();ctx.arc(this.x,this.y,this.radius*4,0,Math.PI*2);ctx.fillStyle=glow;ctx.fill();}ctx.beginPath();ctx.arc(this.x,this.y,this.radius,0,Math.PI*2);ctx.fillStyle=`rgba(${r},${g},${b},${a})`;ctx.fill();if(this.isBrightStar){const ray=this.radius*3.2;ctx.beginPath();ctx.moveTo(this.x-ray,this.y);ctx.lineTo(this.x+ray,this.y);ctx.moveTo(this.x,this.y-ray);ctx.lineTo(this.x,this.y+ray);ctx.strokeStyle=`rgba(${r},${g},${b},${a*.42})`;ctx.lineWidth=.7;ctx.stroke();} }
  }

  function visibleCanvas(canvas){
    const overlay=canvas.closest('.agentgo-monitor-overlay,.agentgo-mail-overlay');
    if(overlay) return overlay.classList.contains('open');
    return !document.querySelector('.agentgo-monitor-overlay.open,.agentgo-mail-overlay.open');
  }

  function particleCount(state){ const area=state.width*state.height;const desktop=Math.floor(area/16500);const maximum=state.width<700?65:115;return Math.min(Math.max(desktop,35),maximum); }

  function initializeCanvas(canvas){
    if(!canvas||states.has(canvas))return states.get(canvas);
    const ctx=canvas.getContext('2d'); if(!ctx)return null;
    const state={canvas,ctx,width:0,height:0,pixelRatio:1,particles:[],shootingStars:[]};
    states.set(canvas,state); resizeState(state); return state;
  }

  function resizeState(state){
    const rect=state.canvas.getBoundingClientRect();
    state.width=Math.max(1,Math.round(rect.width||window.innerWidth));state.height=Math.max(1,Math.round(rect.height||window.innerHeight));state.pixelRatio=Math.min(window.devicePixelRatio||1,2);state.canvas.width=Math.floor(state.width*state.pixelRatio);state.canvas.height=Math.floor(state.height*state.pixelRatio);state.ctx.setTransform(state.pixelRatio,0,0,state.pixelRatio,0,0);state.particles=[];state.shootingStars=[];for(let i=0;i<particleCount(state);i++)state.particles.push(new Particle(state));for(let i=0;i<2;i++)state.shootingStars.push(new ShootingStar(state));
  }

  function drawConnections(state){
    const {ctx,particles,width}=state;const connectionDistance=width<700?95:130;const mouseConnectionDist=180;
    for(let i=0;i<particles.length;i++){
      const first=particles[i];
      if(mouse.x!==null){const dx=first.x-mouse.x,dy=first.y-mouse.y,distSq=dx*dx+dy*dy;if(distSq<mouseConnectionDist*mouseConnectionDist&&distSq>0){const dist=Math.sqrt(distSq),opacity=.15*(1-dist/mouseConnectionDist);ctx.beginPath();ctx.moveTo(first.x,first.y);ctx.lineTo(mouse.x,mouse.y);ctx.strokeStyle=`rgba(168,140,255,${opacity})`;ctx.lineWidth=1;ctx.stroke();const pull=.05*(1-dist/mouseConnectionDist);first.vx-=(dx/dist)*pull;first.vy-=(dy/dist)*pull;}}
      for(let j=i+1;j<particles.length;j++){const second=particles[j],dx=first.x-second.x,dy=first.y-second.y,distSq=dx*dx+dy*dy;if(distSq<connectionDistance*connectionDistance&&distSq>0){const dist=Math.sqrt(distSq),opacity=.105*(1-dist/connectionDistance);ctx.beginPath();ctx.moveTo(first.x,first.y);ctx.lineTo(second.x,second.y);ctx.strokeStyle=`rgba(90,200,255,${opacity})`;ctx.lineWidth=.75;ctx.stroke();const force=(connectionDistance-dist)/connectionDistance,pull=force*.015,fx=dx/dist,fy=dy/dist;first.vx-=fx*pull;first.vy-=fy*pull;second.vx+=fx*pull;second.vy+=fy*pull;if(dist<(first.baseRadius+second.baseRadius+1)){second.reset(true);first.radius=Math.min(first.radius+1.2,first.baseRadius*3);}}}
    }
  }

  function animate(ts=0){
    states.forEach(state=>{if(!visibleCanvas(state.canvas))return;const {ctx,width,height}=state;ctx.clearRect(0,0,width,height);drawConnections(state);state.particles.forEach(p=>{p.update();p.draw(ctx,ts);});state.shootingStars.forEach(s=>{s.update();s.draw(ctx);});});
    raf=requestAnimationFrame(animate);
  }

  function refresh(){ document.querySelectorAll('canvas[data-yeti-stars]').forEach(initializeCanvas); }

  window.addEventListener('resize',()=>{clearTimeout(resizeTimer);resizeTimer=setTimeout(()=>states.forEach(resizeState),150);});
  const observer=new MutationObserver(refresh);observer.observe(document.documentElement,{childList:true,subtree:true});
  window.AgentGOYetiStars={refresh};
  refresh(); if(!raf)raf=requestAnimationFrame(animate);
})();
