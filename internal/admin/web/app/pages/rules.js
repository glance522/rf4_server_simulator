// 玩法规则：决定钓鱼「玩起来是什么感觉」。数值微调在「数值调整」页。
import { h, api, post, icon, toast, errText, pageHead, section, callout, badge, Form, savebar, setLeaveGuard, confirmDialog, render } from '../core.js';

const STYLES = [
  ['realistic', '真实鱼汛', '鱼按习性、天气、位置和饵料决定咬不咬。要研究钓点、选对饵，更接近真实钓鱼。', '下面六项规则中，除欢乐时光外全部开启'],
  ['legacy', '全随机', '任何鱼都可能咬任何饵，出鱼完全随机。轻松，适合随便玩玩。', '下面六项规则中，除欢乐时光外全部关闭'],
];
export const RULE_SWITCHES = [
  ['bite_gate_enabled', '饵要对口', '拟饵不会钓到不吃拟饵的鱼（如泥鳅、鲤科鱼）；钓层不对时几乎不咬。', '0'],
  ['fish_ecology_enabled', '鱼种习性', '252 种鱼各按习性决定咬不咬：水层、昼夜、季节、天气、水底环境、拟饵动作都有影响。', '0'],
  ['fish_weather_enabled', '天气与水温', '游戏里的气温和天气会影响鱼口；底钓、浮钓时鱼会断断续续地试口。', '0'],
  ['fish_distribution_enabled', '鱼群分布', '每种鱼只在地图的特定区域出没，每 4 个游戏小时重新分布一次。', '0'],
  ['fish_bait_rules_enabled', '鱼会挑饵', '根据钩上挂的饵决定会来哪些鱼。每种鱼爱吃什么在「鱼饵偏好」里设置。', '0'],
  ['happy_hour_enabled', '欢乐时光', '会员在线钓鱼时：经验 +50%、鱼口 +10%、熟练度提升机会 +25%，没有每日时长限制。', '1'],
];
const SPAWN = [
  ['grade_ratio', '按比例出鱼', '直接设定未达标、合格、奖杯、蓝冠四档鱼各占多少。最直观。'],
  ['fish_hunt', '鱼猎四阶段', '淡水按「日常 → 聚鱼 → 发窝 → 巨物」四个阶段逐步出好鱼，海钓和路亚同样适用。'],
  ['two_stage', '日常 / 打窝两档', '底钓、浮钓只有「日常」和「打窝后」两档，各自设定比例。海钓、路亚仍按四阶段。'],
];
const GRADES = [['below', '未达标', '#7f8b9a'], ['qualified', '合格', '#4cb3d4'], ['trophy', '奖杯', '#e0a743'], ['super', '蓝冠', '#9d7cf0']];
const GRADE_SETS = {
  grade_ratio: [{ prefix: 'grade_pct_', title: '四档比例', defs: [3, 87.5, 9, 0.5] }],
  two_stage: [
    { prefix: 'two_stage_daily_', title: '日常（没打窝）', defs: [10, 86.5, 3, 0.5] },
    { prefix: 'two_stage_baited_', title: '打窝后', defs: [1, 92, 6, 1] },
  ],
};
const OTHER = [
  ['condition_health_enabled', '饥饿和寒冷会掉血', '关闭后生命值始终是满的，药品也不会消耗。', '1'],
  ['condition_fishing_pause_enabled', '钓鱼时精力不恢复', '竿在水里时精力暂停恢复，收竿后再恢复。关闭则一直正常恢复。', '1'],
  ['hook_nibble_enabled', '鱼会先嘬饵', '鱼咬钩前先轻嘬几下，抬竿太早会钓空。关闭后第一次抬竿不会判嘬饵。', '0'],
  ['bite_pace_enabled', '饵效控速', '开启后饵好不好决定来鱼快慢：对口饵不拖慢，不对口的饵拉长等待并降低咬口概率。只控节奏，不改出什么鱼。', '0'],
];

export default async function rules() {
  const [t, mode] = await Promise.all([api('/api/tunables'), api('/api/fish/bite_mode').catch(() => ({}))]);
  const val = (k, d) => (t[k] === undefined || t[k] === '' ? d : t[k]);
  const form = new Form();
  savebar(form, {
    onSave: async (values) => {
      for (const [key, value] of Object.entries(values)) await post('/api/tunables', { key, value: String(value) });
      toast('已保存，约 30 秒内生效');
      render();
    },
  });
  setLeaveGuard(() => form.isDirty());

  // 玩法风格
  const cur = mode.mode || 'legacy';
  const styleCards = h('div', { class: 'grid-2' }, STYLES.map(([m, title, desc, note]) => {
    const on = cur === m;
    return h('button', {
      type: 'button', class: 'card', style: { textAlign: 'left', padding: '18px 20px', cursor: on ? 'default' : 'pointer', borderColor: on ? 'var(--accent)' : '', background: on ? 'var(--accent-soft)' : '' },
      onclick: async () => {
        if (on) return;
        const ok = await confirmDialog({ title: `切换到「${title}」？`, desc: `${note}。约 30 秒内生效。`, confirm: '切换' });
        if (!ok) return;
        try { await post('/api/fish/bite_mode', { mode: m }); toast(`已切换到「${title}」`); render(); } catch (e) { toast(errText(e), 'bad'); }
      },
    }, h('div', { class: 'row', style: { justifyContent: 'space-between' } }, h('b', { style: { fontSize: '15px' }, text: title }), on ? badge('当前', 'accent') : null),
    h('p', { class: 'text-2 small', style: { marginTop: '6px' }, text: desc }));
  }));

  // 出鱼方式
  const spawnMode = val('grade_ratio_enabled', '1') === '1' ? 'grade_ratio' : val('two_stage_hunt_enabled', '0') === '1' ? 'two_stage' : val('fish_hunt_enabled', '0') === '1' ? 'fish_hunt' : '';
  const spawnCards = h('div', { class: 'grid-3' }, SPAWN.map(([m, title, desc]) => {
    const on = spawnMode === m;
    return h('button', {
      type: 'button', class: 'card', style: { textAlign: 'left', padding: '16px 18px', cursor: on ? 'default' : 'pointer', borderColor: on ? 'var(--accent)' : '', background: on ? 'var(--accent-soft)' : '' },
      onclick: async () => {
        if (on) return;
        if (form.isDirty() && !(await confirmDialog({ title: '有修改还没保存', desc: '切换出鱼方式会刷新页面，没保存的修改会丢失。', confirm: '继续切换' }))) return;
        try { await post('/api/fish/spawn_mode', { mode: m }); toast(`已切换到「${title}」，约 30 秒内生效`); render(); } catch (e) { toast(errText(e), 'bad'); }
      },
    }, h('div', { class: 'row', style: { justifyContent: 'space-between' } }, h('b', { text: title }), on ? badge('当前', 'accent') : null),
    h('p', { class: 'muted small', style: { marginTop: '6px' }, text: desc }));
  }));
  const gradeEditors = (GRADE_SETS[spawnMode] || []).map((set) => gradeEditor(form, set, t));

  return h('div', { class: 'page' },
    pageHead('玩法规则', '决定钓鱼玩起来是什么感觉。具体数字（咬钩快慢、经验倍率等）在「数值调整」里改。'),
    section({
      title: '玩法风格', desc: '一键切换下面六项规则的组合。',
      actions: cur === 'mixed' ? badge('自定义组合', 'warn') : null,
      body: h('div', { class: 'stack' }, styleCards, cur === 'mixed' ? callout('info', '下面的规则被单独改过，不属于任何一种预设。选择上面任一风格会整体覆盖。') : null),
    }),
    section({ title: '规则', desc: '每一项都可以单独开关，约 30 秒内生效。', body: h('div', {}, RULE_SWITCHES.map(([k, l, d, def]) => form.row({ key: k, label: l, help: d, kind: 'bool', def }, val(k, def)))) }),
    section({
      title: '出鱼方式', desc: '决定钓到的鱼里，好鱼（奖杯、蓝冠）占多少。三选一，点卡片切换。',
      body: h('div', { class: 'stack' }, spawnCards, ...gradeEditors,
        spawnMode === 'fish_hunt' ? callout('info', '鱼猎四阶段按玩家打窝、聚鱼的进度自动推进，没有需要手动设置的比例。') : null,
        !spawnMode ? callout('warn', '还没有选择出鱼方式，目前按旧版规则出鱼。') : null),
    }),
    section({ title: '其他', body: h('div', {},
      OTHER.map(([k, l, d, def]) => form.row({ key: k, label: l, help: d, kind: 'bool', def }, val(k, def))),
      form.row({ key: 'hook_spawn_match_strength', label: '鱼钩大小影响出鱼', help: '0 表示不影响；1 表示大钩更容易钓到大鱼、小钩更容易钓到小鱼；2 更明显。选「按比例出鱼」时不起作用。', kind: 'float', min: 0, max: 2, def: '0' }, val('hook_spawn_match_strength', '0'))) }));
}

// 四档比例编辑：输入相对权重，实时显示换算后的百分比
function gradeEditor(form, set, t) {
  const bar = h('div', { style: { display: 'flex', height: '10px', borderRadius: '6px', overflow: 'hidden', background: 'var(--surface-4)' } });
  const legend = h('div', { class: 'row small', style: { gap: '16px' } });
  const fields = GRADES.map(([g, label], i) => {
    const key = set.prefix + g;
    return { key, label, f: form.field({ key, label, kind: 'float', min: 0, max: 100 }, t[key] ?? String(set.defs[i])) };
  });
  const update = () => {
    const nums = fields.map(({ key }) => Math.max(0, Number(form.value(key)) || 0));
    const sum = nums.reduce((a, b) => a + b, 0) || 1;
    bar.replaceChildren(...nums.map((n, i) => h('div', { style: { width: (n / sum * 100) + '%', background: GRADES[i][2] } })));
    legend.replaceChildren(...nums.map((n, i) => h('span', {}, h('span', { style: { display: 'inline-block', width: '8px', height: '8px', borderRadius: '2px', background: GRADES[i][2], marginRight: '6px' } }),
      GRADES[i][1] + ' ', h('b', { text: (n / sum * 100).toFixed(1) + '%' }))));
  };
  const inputs = h('div', { class: 'grid-4' }, fields.map(({ label, f }) => {
    f.input = h('input', { class: 'input num', inputmode: 'decimal', value: f.original, oninput: () => { form.refresh(); update(); } });
    f.errEl = h('div', { class: 'help', style: { color: '#f08a97' }, hidden: true });
    f.row = h('div', { class: 'field' }, h('label', { text: label }), f.input, f.errEl);
    return f.row;
  }));
  update();
  return h('div', { class: 'card', style: { padding: '16px 18px', background: 'var(--surface-2)' } },
    h('div', { class: 'row', style: { justifyContent: 'space-between', marginBottom: '10px' } }, h('b', { text: set.title }), h('span', { class: 'muted small', text: '填相对数量，会自动换算成百分比' })),
    inputs, h('div', { style: { margin: '14px 0 8px' } }, bar), legend);
}
