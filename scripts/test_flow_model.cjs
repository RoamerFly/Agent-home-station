'use strict';
const test=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),path=require('node:path');
const M=require('../internal/admin/static/flow-model.js');
const template=()=>M.normalize(JSON.parse(fs.readFileSync(path.join(__dirname,'../internal/admin/static/flow-default.json'),'utf8')));
test('default diagram covers the current incoming, autonomous and delivery paths',()=>{
 const d=template(),c=M.coverage(d);assert.equal(d.nodes.length,24);assert.equal(c.present,c.total);
 for(const [a,b]of [['receive','inbox'],['quote','aggregate'],['observe','memory'],['tools','feedback'],['feedback','agent'],['receipt','retry'],['retry','send'],['tasks','agent'],['silent','wake'],['wake','memory']])assert(d.edges.some(e=>e.from===a&&e.to===b),a+' -> '+b);
});
test('delete cascades only diagram edges and original remains available for undo',()=>{
 const original=template(),next=M.removeNode(original,'agent');assert(original.nodes.some(n=>n.id==='agent'));assert(!next.nodes.some(n=>n.id==='agent'));assert(!next.edges.some(e=>e.from==='agent'||e.to==='agent'));assert.equal(M.normalize(next).nodes.length,23);
});
test('import validates endpoints, kinds, extra configuration, positions and duplicate IDs',()=>{
 for(const change of [d=>d.nodes[0].kind='execute',d=>d.nodes[0].x=Infinity,d=>d.nodes[0].x=100001,d=>d.nodes[1].id=d.nodes[0].id,d=>d.edges[0].to='absent',d=>d.api_key='synthetic',d=>d.nodes[0].command='run']){const d=template();change(d);assert.throws(()=>M.normalize(d))}
 const d=template();d.nodes[0].title='<img src=x onerror=bad>';assert.equal(M.parse(JSON.stringify(d)).nodes[0].title,d.nodes[0].title,'labels are plain text, never HTML');
});
test('cycles, Unicode, negative coordinates and empty designs survive export/import',()=>{
 const d=template();d.nodes.push({id:'note-test',kind:'note',title:'后续的想法',note:'中文与 🐳',tone:'teal',x:-128.5,y:42.25});d.edges.push({id:'self-edge',from:'note-test',to:'note-test',label:'继续想想'});assert.deepEqual(M.parse('\uFEFF'+JSON.stringify(d)),d);assert.deepEqual(M.normalize({version:1,name:'空画布',nodes:[],edges:[]}).nodes,[]);
});
test('arrange retains content, valid curves and positions added planning nodes without overlap',()=>{
 const d=template();d.nodes.push({id:'custom',kind:'action',title:'想法',note:'规划',tone:'amber',x:1,y:1});const after=M.arrange(d,template());assert.equal(after.nodes.at(-1).note,'规划');assert(after.nodes.at(-1).y>=900);for(const e of after.edges){const a=after.nodes.find(n=>n.id===e.from),b=after.nodes.find(n=>n.id===e.to);assert(!M.edgePath(a,b).includes('NaN'))}
});

test('flowchart and tree routes have arrow endpoints and avoid unrelated node interiors',()=>{
 for(const mode of ['chart','tree']){
  const d=M.arrange(template(),template(),mode),r=M.routeEdges(d);assert.equal(r.degraded,false);assert.equal(r.points.size,d.edges.length);
  for(const a of d.nodes)for(const b of d.nodes)if(a.id!==b.id)assert(!(a.x<b.x+M.W&&a.x+M.W>b.x&&a.y<b.y+M.H&&a.y+M.H>b.y),'overlapping '+mode+' nodes');
  for(const e of d.edges){const points=r.points.get(e.id);assert(points.length>=2);assert(!r.paths.get(e.id).includes('NaN'));
   for(let i=1;i<points.length;i++){const a=points[i-1],b=points[i];assert(a.x===b.x||a.y===b.y,'non-orthogonal segment');for(const n of d.nodes){if(n.id===e.from||n.id===e.to)continue;const hits=a.x===b.x?a.x>n.x&&a.x<n.x+M.W&&Math.max(a.y,b.y)>n.y&&Math.min(a.y,b.y)<n.y+M.H:a.y>n.y&&a.y<n.y+M.H&&Math.max(a.x,b.x)>n.x&&Math.min(a.x,b.x)<n.x+M.W;assert(!hits,mode+' '+e.id+' passes through '+n.id)}}
   const end=points.at(-1),target=d.nodes.find(n=>n.id===e.to);assert(end.x>=target.x&&end.x<=target.x+M.W&&end.y>=target.y&&end.y<=target.y+M.H,'arrow ends outside target');
  }
 }
 assert.equal(M.edgeRole({from:'model'}),'support');assert.equal(M.edgeRole({from:'retry'}),'return');assert.equal(M.edgeRole({from:'receive'}),'main');
 const d=template();d.edges.push({id:'self-edge',from:'agent',to:'agent',label:'再思考'});assert(M.routeEdges(d).points.has('self-edge'));
});
