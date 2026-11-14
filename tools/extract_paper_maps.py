"""Extract in-game paper map textures for the admin fish-zone map editor.

Optional dependencies: UnityPy 1.25.4 (MIT), Pillow (HPND).
See docs/admin-console.md. The client install is only read; exported WebP
files are committed to internal/admin/web/maps so the launcher embeds them
(unlike the water-mask tool, these images ship with the admin console).

Expected texture names are parsed from internal/fishmaps/fishmaps.go
(paperMapLevels + textureNameExceptions) — the same single source the
server reads. Run:

    python tools/extract_paper_maps.py \
        --client-data 'D:/Games/rf4game/Game/rf4_x64_Data' [--force]
"""
import argparse
import json
import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parents[1]

try:
    import UnityPy
except ImportError:  # pragma: no cover - environment guard
    sys.exit('需要 UnityPy 1.25.4：pip install UnityPy==1.25.4')


def expected_textures_from_source():
    source = (ROOT / 'internal/fishmaps/fishmaps.go').read_text(encoding='utf8')
    levels_block = source.split('var paperMapLevels = map[string]bool{', 1)[1].split('\n}', 1)[0]
    levels = re.findall(r'"(level_[^"]+)":\s*true', levels_block)
    if not levels:
        raise ValueError('paperMapLevels 解析为空：fishmaps.go 结构可能已变化')
    exc_block = source.split('var textureNameExceptions = map[string]string{', 1)[1].split('\n}', 1)[0]
    exceptions = dict(re.findall(r'"(level_[^"]+)":\s*"(map_[^"]+)"', exc_block))
    expected = {}
    for level in levels:
        expected[exceptions.get(level, 'map_' + level)] = level
    return expected


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument('--client-data', type=pathlib.Path,
                        default=pathlib.Path('D:/Games/rf4game/Game/rf4_x64_Data'),
                        help='客户端 rf4_x64_Data 目录')
    parser.add_argument('--out', type=pathlib.Path,
                        default=ROOT / 'internal/admin/web/maps',
                        help='WebP 输出目录（随 admin web 内嵌）')
    parser.add_argument('--force', action='store_true', help='重新导出已存在的文件')
    args = parser.parse_args()

    bundle = args.client_data / 'Data' / 'maps'
    if not bundle.is_file():
        sys.exit(f'找不到客户端 maps bundle: {bundle}')
    if args.out.resolve().is_relative_to(args.client_data.resolve()):
        sys.exit('输出目录不得落在客户端安装目录内')

    expected = expected_textures_from_source()
    print(json.dumps({'expected': len(expected)}, ensure_ascii=False))
    args.out.mkdir(parents=True, exist_ok=True)

    env = UnityPy.load(str(bundle))
    found = set()
    written, skipped, total_bytes = [], 0, 0
    for obj in env.objects:
        if obj.type.name != 'Texture2D':
            continue
        data = obj.read()
        name = data.m_Name
        if name not in expected:
            continue
        found.add(name)
        path = args.out / f'{name}.webp'
        if path.exists() and not args.force:
            skipped += 1
            total_bytes += path.stat().st_size
            print(json.dumps({'skip': name}, ensure_ascii=False))
            continue
        image = data.image
        if image.mode != 'RGB':
            image = image.convert('RGB')
        image.save(path, 'WEBP', quality=80)
        written.append(name)
        total_bytes += path.stat().st_size
        print(json.dumps({'write': name, 'size': path.stat().st_size}, ensure_ascii=False))

    missing = sorted(set(expected) - found)
    print(json.dumps({'written': len(written), 'skipped': skipped,
                      'missing': missing, 'bytes': total_bytes}, ensure_ascii=False))
    if missing:
        sys.exit(f'客户端缺少 {len(missing)} 张期望贴图: {missing}')


if __name__ == '__main__':
    main()
