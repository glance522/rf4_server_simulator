"""Generate a local five-point water mask without running or modifying RF4.

Optional dependencies: UnityPy 1.25.4 (MIT), NumPy (BSD-3-Clause).
See docs/fish-query-water.md. Client assets and generated masks stay local.
"""
import argparse
import gc
import hashlib
from datetime import datetime, timezone
import json
import math
import pathlib
import re
import struct

import numpy as np
import UnityPy
from UnityPy.helpers.MeshHelper import MeshHandler

ROOT = pathlib.Path(__file__).resolve().parents[1]
CELL_SIZE = 40
SAMPLING = 'center_corners_any'

def sample_grid(bounds):
    x0,z0,x1,z1 = bounds
    if not all(math.isfinite(v) for v in bounds) or x0 >= x1 or z0 >= z1:
        raise ValueError('Invalid scan bounds')
    ix0,iz0 = math.floor(x0/CELL_SIZE),math.floor(z0/CELL_SIZE)
    width,height = math.ceil(x1/CELL_SIZE)-ix0,math.ceil(z1/CELL_SIZE)-iz0
    if max(width,height) > 76:
        raise ValueError('Grid exceeds supported dimensions')
    points = []
    for ix in range(ix0,ix0+width):
        for iz in range(iz0,iz0+height):
            a,b = max(x0,ix*CELL_SIZE),max(z0,iz*CELL_SIZE)
            c,d = min(x1,(ix+1)*CELL_SIZE),min(z1,(iz+1)*CELL_SIZE)
            center = ((a+c)/2,(b+d)/2)
            # Keep upper corners inside the half-open cell. nextafter(0, -1)
            # would underflow to -0.0 when divided by the 40-unit bin size.
            c -= min(1e-6,(c-a)/4)
            d -= min(1e-6,(d-b)/4)
            points.extend([center,(a,b),(c,b),(a,d),(c,d)])
    return ix0,iz0,width,height,np.asarray(points,dtype=np.float64)

def sample_heights(heights, origin, scale, points):
    n = heights.shape[0]
    x,z = (points[:,0]-origin[0])/scale['x'],(points[:,1]-origin[2])/scale['z']
    inside = (x >= 0)&(z >= 0)&(x <= n-1)&(z <= n-1)
    x,z = np.clip(x,0,n-1),np.clip(z,0,n-1)
    a,b = np.minimum(x.astype(int),n-2),np.minimum(z.astype(int),n-2)
    fx,fz = x-a,z-b
    values = ((1-fx)*(1-fz)*heights[b,a] + fx*(1-fz)*heights[b,a+1] + (1-fx)*fz*heights[b+1,a] + fx*fz*heights[b+1,a+1])*scale['y'] + origin[1]
    return np.where(inside,values,-np.inf)

def encode_samples(wet):
    return (np.asarray(wet,dtype=np.uint8).reshape(-1,5) * np.array([1,2,4,8,16],dtype=np.uint8)).sum(axis=1).astype(int).tolist()

class TriangleSurface:
    def __init__(self, vertices, triangles, bounds):
        self.triangles = np.asarray(vertices)[np.asarray(triangles,dtype=int)][:,:,[0,2]]
        lower,upper = self.triangles.min(axis=1),self.triangles.max(axis=1)
        self.bins = {}
        ix0,iz0 = math.floor(bounds[0]/CELL_SIZE),math.floor(bounds[1]/CELL_SIZE)
        ix1,iz1 = math.ceil(bounds[2]/CELL_SIZE)-1,math.ceil(bounds[3]/CELL_SIZE)-1
        for i in np.flatnonzero((upper[:,0] >= bounds[0])&(upper[:,1] >= bounds[1])&(lower[:,0] <= bounds[2])&(lower[:,1] <= bounds[3])):
            for ix in range(max(ix0,math.floor(lower[i,0]/CELL_SIZE)),min(ix1,math.floor(upper[i,0]/CELL_SIZE))+1):
                for iz in range(max(iz0,math.floor(lower[i,1]/CELL_SIZE)),min(iz1,math.floor(upper[i,1]/CELL_SIZE))+1):
                    self.bins.setdefault((ix,iz),[]).append(i)
        self.bins = {key:self.triangles[value] for key,value in self.bins.items()}

    def contains(self, points):
        result = np.zeros(len(points),dtype=bool)
        for i,(x,z) in enumerate(points):
            triangles = self.bins.get((math.floor(x/CELL_SIZE),math.floor(z/CELL_SIZE)))
            if triangles is None:
                continue
            a,b,c = triangles[:,0],triangles[:,1],triangles[:,2]
            area = (b[:,0]-a[:,0])*(c[:,1]-a[:,1])-(b[:,1]-a[:,1])*(c[:,0]-a[:,0])
            s1 = (b[:,0]-a[:,0])*(z-a[:,1])-(b[:,1]-a[:,1])*(x-a[:,0])
            s2 = (c[:,0]-b[:,0])*(z-b[:,1])-(c[:,1]-b[:,1])*(x-b[:,0])
            s3 = (a[:,0]-c[:,0])*(z-c[:,1])-(a[:,1]-c[:,1])*(x-c[:,0])
            result[i] = np.any((np.abs(area)>1e-10)&(((s1>=-1e-7)&(s2>=-1e-7)&(s3>=-1e-7))|((s1<=1e-7)&(s2<=1e-7)&(s3<=1e-7))))
        return result

def component_payload(component):
    raw = component.object_reader.get_raw_data()
    length = struct.unpack_from('<i',raw,28)[0]
    if length < 0 or raw[32:32+length].decode('utf8') != component.m_Name:
        raise ValueError('Unexpected component serialization')
    return raw[(32+length+3)&~3:]

def scenes_from_source():
    # 标定表自 2026-11 起以 internal/fishmaps 为单一事实源（gameserver 只是别名引用）。
    source = (ROOT / 'internal/fishmaps/fishmaps.go').read_text(encoding='utf8')
    block = source.split('var Scenes = map[string]Scene{', 1)[1].split('\n}', 1)[0]
    result = {}
    for line in block.splitlines():
        match = re.match(r'\s*"(level_[^"]+)":\s*\{(.*)\},', line)
        if not match:
            if line.strip():
                raise ValueError('Unrecognized scene declaration: ' + line)
            continue
        values = {k: float(v) for k,v in re.findall(r'(OriginX|OriginZ|Size):\s*([-+\d.eE]+)', match[2])}
        paper = re.search(r'PaperMap\{([^}]+)\}', match[2])
        p = [float(v.strip()) for v in paper[1].split(',')]
        ox, oz = values.get('OriginX', 0.), values.get('OriginZ', 0.)
        px, pz, scale = (struct.unpack('<f', struct.pack('<f', v))[0] for v in p[:3])
        result[match[1]] = {'origin_x': ox, 'origin_z': oz, 'size': values['Size'], 'bounds': [max(ox, ox-px/scale), max(oz, oz-pz/scale), min(ox+values['Size'], ox+(p[3]-px)/scale), min(oz+values['Size'], oz+(p[4]-pz)/scale)]}
    if not result:
        raise ValueError('No supported scene definitions')
    return result

def local_matrix(transform):
    p, s, q = transform.m_LocalPosition, transform.m_LocalScale, transform.m_LocalRotation
    x,y,z,w = q.x,q.y,q.z,q.w
    rotation = np.array([[1-2*(y*y+z*z), 2*(x*y-z*w), 2*(x*z+y*w)], [2*(x*y+z*w), 1-2*(x*x+z*z), 2*(y*z-x*w)], [2*(x*z-y*w), 2*(y*z+x*w), 1-2*(x*x+y*y)]])
    matrix = np.eye(4)
    matrix[:3,:3] = rotation @ np.diag([s.x,s.y,s.z])
    matrix[:3,3] = [p.x,p.y,p.z]
    return matrix

class Scene:
    def __init__(self, folder, index):
        self.env = UnityPy.load(str(folder / f'level{index}'))
        self.objects = list(self.env.objects)
        self.scripts = {}
        self.go_cache = {}

    def script_name(self, component):
        ptr = component.m_Script
        key = (ptr.m_FileID, ptr.m_PathID)
        if key not in self.scripts:
            self.scripts[key] = ptr.read().m_ClassName if ptr.m_PathID else ''
        return self.scripts[key]

    def game_object(self, ptr):
        key = (ptr.m_FileID, ptr.m_PathID)
        if key not in self.go_cache:
            go = ptr.read()
            components = [p.component.deref().read(check_read=False) for p in go.m_Component]
            tr = next(c for c in components if c.object_reader.type.name == 'Transform')
            matrix = local_matrix(tr)
            active = go.m_IsActive
            chain = tr.m_Father
            while chain.m_PathID:
                parent = chain.read()
                matrix = local_matrix(parent) @ matrix
                active = active and parent.m_GameObject.read().m_IsActive
                chain = parent.m_Father
            self.go_cache[key] = (go, components, matrix, active)
        return self.go_cache[key]

    def controlled_objects(self):
        controlled = set()
        for obj in self.objects:
            if obj.type.name != 'MonoBehaviour':
                continue
            c = obj.read(check_read=False)
            if self.script_name(c) != 'WaterbodyController' or not c.m_Enabled:
                continue
            if not self.game_object(c.m_GameObject)[3]:
                continue
            payload = component_payload(c)
            offset = 0
            for _ in range(2):
                count = struct.unpack_from('<i',payload,offset)[0]
                offset += 4
                if count < 0 or count > 64 or offset+count*24 > len(payload):
                    raise ValueError('Unsupported water controller layout')
                for _ in range(count*2):
                    file_id,path_id = struct.unpack_from('<iq',payload,offset)
                    offset += 12
                    if path_id:
                        if file_id != 0:
                            raise ValueError('External water-controller alternative')
                        controlled.add(path_id)
            if len(payload)-offset != 4:
                raise ValueError('Unexpected water controller tail')
        return controlled

    def terrain_heights(self, points):
        values = np.full(len(points),-np.inf)
        count = 0
        for obj in self.objects:
            if obj.type.name != 'Terrain':
                continue
            c = obj.read()
            _,_,matrix,active = self.game_object(c.m_GameObject)
            if not active or not c.m_Enabled:
                continue
            data = c.m_TerrainData.read_typetree()['m_Heightmap']
            if data['m_Holes']:
                raise ValueError('Terrain holes require separate handling')
            n = data['m_Resolution']
            heights = np.asarray(data['m_Heights'],dtype=np.float64).reshape(n,n)
            patches = np.asarray(data['m_MinMaxPatchHeights'])
            # Current Unity serialized heights use a 32766 denominator; verify
            # against the independently serialized normalized patch extrema.
            if not np.allclose([heights.min()/32766,heights.max()/32766],[patches.min(),patches.max()],rtol=0,atol=2e-7):
                raise ValueError('Unrecognized terrain height encoding')
            heights /= 32766
            values = np.maximum(values,sample_heights(heights,matrix[:3,3],data['m_Scale'],points))
            count += 1
        if not count or not np.isfinite(values).all():
            raise ValueError('Some sample points lack terrain coverage')
        return values,count

    def sample_water(self, points, ground, bounds):
        controlled = self.controlled_objects()
        wet = np.zeros(len(points),dtype=bool)
        details = []
        for obj in self.objects:
            if obj.type.name != 'MonoBehaviour':
                continue
            c = obj.read(check_read=False)
            name = self.script_name(c)
            if name not in {'Water2','Water4_Interface','Water5_Interface','Water5_Interface49'} or not c.m_Enabled:
                continue
            go,components,matrix,active = self.game_object(c.m_GameObject)
            controlled_candidate = go.object_reader.path_id in controlled
            tr = next(v for v in components if v.object_reader.type.name == 'Transform')
            parent_ptr = tr.m_Father
            while not controlled_candidate and parent_ptr.m_PathID:
                parent = parent_ptr.read()
                controlled_candidate = parent.m_GameObject.m_PathID in controlled
                parent_ptr = parent.m_Father
            if not active and not controlled_candidate:
                continue
            if not np.allclose(matrix[1,[0,2]],0,atol=1e-6):
                raise ValueError('Tilted water surface requires separate handling')
            level = float(matrix[1,3])
            # Use the geometric baseline shared with the water trigger. Wave
            # and tide displacements are deliberately outside this coarse mask.
            boxes = [v for v in components if v.object_reader.type.name == 'BoxCollider' and v.m_Enabled and v.m_IsTrigger]
            if not boxes:
                raise ValueError('Water body lacks a supported trigger footprint')
            homogeneous = np.column_stack([points[:,0],np.full(len(points),level),points[:,1],np.ones(len(points))])
            local = homogeneous @ np.linalg.inv(matrix).T
            inside = np.zeros(len(points),dtype=bool)
            for box in boxes:
                center,size = box.m_Center,box.m_Size
                if abs(center.y+size.y/2) > .01:
                    raise ValueError('Trigger top differs from the baseline water surface')
                inside |= (np.abs(local[:,0]-center.x) <= size.x/2)&(np.abs(local[:,2]-center.z) <= size.z/2)
            if name == 'Water4_Interface':
                filters = [v for v in components if v.object_reader.type.name == 'MeshFilter' and v.m_Mesh.m_PathID]
                if len(filters) != 1:
                    raise ValueError('Water4 surface mesh is ambiguous')
                mesh = filters[0].m_Mesh.read()
                handler = MeshHandler(mesh)
                handler.process()
                vertices = np.asarray(handler.m_Vertices,dtype=np.float64)
                vertices = vertices @ matrix[:3,:3].T + matrix[:3,3]
                if np.max(np.abs(vertices[:,1]-level)) > .001:
                    raise ValueError('Non-planar water mesh requires separate handling')
                triangles = [tri for submesh in handler.get_triangles() for tri in submesh]
                footprint = TriangleSurface(vertices,triangles,bounds)
                inside &= footprint.contains(points)
            # A broad trigger alone is not water: land above the surface is
            # always rejected. The remaining mask intentionally stays coarse.
            selected = inside & (ground < level)
            wet |= selected
            details.append({'type':name,'level':level,'controlled_alternative':not bool(active),'wet_points':int(selected.sum()),'footprint':'mesh_and_trigger' if name == 'Water4_Interface' else 'trigger'})
        if not details:
            raise ValueError('No supported active or controlled water body')
        return wet,details

def build_grid(scene, definition):
    bounds = definition['bounds']
    ix,iz,width,height,points = sample_grid(bounds)
    ground,terrain_count = scene.terrain_heights(points)
    wet,bodies = scene.sample_water(points,ground,bounds)
    samples = encode_samples(wet)
    grid = {'bounds':bounds,'min_x':ix,'min_z':iz,'width':width,'height':height,'samples':samples}
    audit = {'cells':len(samples),'kept':sum(v != 0 for v in samples),'removed':sum(v == 0 for v in samples),'all_five_water':sum(v == 31 for v in samples),'terrain_count':terrain_count,'water':bodies}
    return grid,audit

def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--client-data', type=pathlib.Path, required=True)
    parser.add_argument('--output', type=pathlib.Path, required=True)
    parser.add_argument('--audit', type=pathlib.Path)
    parser.add_argument('maps', nargs='*')
    args = parser.parse_args()
    for path in [args.output,args.audit]:
        if path and path.resolve().is_relative_to(args.client_data.resolve().parent):
            parser.error('Output must not be inside the client installation')
    supported = scenes_from_source()
    if set(args.maps)-set(supported):
        parser.error('Unknown map: ' + ', '.join(sorted(set(args.maps)-set(supported))))
    requested = set(args.maps) if args.maps else set(supported)
    build = UnityPy.load(str(args.client_data / 'globalgamemanagers'))
    paths = next(o.read_typetree()['scenes'] for o in build.objects if o.type.name == 'BuildSettings')
    del build
    output = {'schema_version':1,'cell_size':CELL_SIZE,'sampling':SAMPLING,'generated_at':datetime.now(timezone.utc).isoformat(),'maps':{}}
    audits = {}
    unsupported = {}
    for index, path in enumerate(paths):
        key = pathlib.PurePosixPath(path).stem
        if key not in requested:
            continue
        scene = Scene(args.client_data, index)
        try:
            grid,audit = build_grid(scene,supported[key])
            output['maps'][key] = grid
            audits[key] = audit
            row = {'map':key,**audit}
        except (ValueError,struct.error) as exc:
            unsupported[key] = str(exc)
            row = {'map':key,'unsupported':str(exc)}
        print(json.dumps(row, ensure_ascii=False),flush=True)
        del scene
        gc.collect()
    for key in sorted(requested-set(output['maps'])-set(unsupported)):
        unsupported[key] = 'Scene missing from client BuildSettings'
    output['unsupported'] = unsupported
    args.output.write_text(json.dumps(output,ensure_ascii=False,separators=(',',':')),encoding='utf8')
    if args.audit:
        args.audit.write_text(json.dumps({'maps':audits,'unsupported':unsupported,'data_sha256':hashlib.sha256(args.output.read_bytes()).hexdigest()},ensure_ascii=False,indent=2),encoding='utf8')
    print(json.dumps({'supported':len(output['maps']),'unsupported':len(unsupported),'output':str(args.output)},ensure_ascii=False))

if __name__ == '__main__':
    main()
