"""Map Agent sampling choices to native nodes; query limits from live ComfyUI."""
import copy
import json
import math

GROUPS = {
    'semantic': ('33:25', 'YuE2GenerateMusic', ('temperature', 'top_p', 'top_k', 'repetition_penalty')),
    'render': ('33:8', 'KSampler', ('steps', 'cfg', 'sampler_name', 'scheduler', 'denoise')),
    'score': ('33:24', 'YuE2GenerateABC', ('max_abc_tokens', 'temperature', 'top_p', 'top_k', 'repetition_penalty', 'penalty_window')),
}
MEANINGS = {
    'semantic': 'Music token generation: temperature/top_p/top_k control sampling variety; repetition_penalty discourages repeated tokens. Lower variety is not a guarantee of faithful melody.',
    'render': 'Audio diffusion rendering: steps trades speed for refinement, cfg controls conditioning strength, sampler_name/scheduler choose the backend algorithm, denoise is its noise fraction. More steps is not always better.',
    'score': 'Original-song ABC score planning only. Covers use SheetSage from the supplied recording instead. Token limit and repetition window affect the planned score, not audio duration.',
}


def load_graph(directory, mode):
    if mode not in ('original', 'cover'):
        raise ValueError('invalid_mode')
    return json.loads((directory / ('audio_yue2_music_cover.json' if mode == 'cover' else 'audio_yue2_text_to_music.json')).read_text(encoding='utf8'))


def options(directory, mode, info):
    graph = load_graph(directory, mode)
    result = {'mode': mode, 'native_limits_verified': True, 'groups': {}, 'planning_mode': 'reference' if mode == 'cover' else ('full' if graph['33:31']['inputs']['value'] else 'off')}
    for group, (node, kind, keys) in GROUPS.items():
        if node not in graph:
            continue
        native = info[kind]['input']['required']
        properties = {}
        for key in keys:
            raw = native[key]
            meta = raw[1] if len(raw) > 1 else {}
            field = {'default': graph[node]['inputs'][key], 'type': raw[0] if isinstance(raw[0], str) else 'COMBO'}
            if 'default' in meta:
                field['native_default'] = meta['default']
            field.update({k: meta[k] for k in ('min', 'max', 'step') if k in meta})
            if isinstance(raw[0], list):
                field['enum'] = raw[0]
            elif 'options' in meta:
                field['enum'] = meta['options']
            properties[key] = field
        result['groups'][group] = {'node': node, 'meaning': MEANINGS[group], 'properties': properties}
    return result


def resolve(body, directory, info):
    mode = body.get('mode') or ('cover' if body.get('reference_base64') else 'original')
    spec = options(directory, mode, info)
    planning = body.get('planning_mode', spec['planning_mode'])
    if (mode == 'cover' and planning != 'reference') or (mode == 'original' and planning not in ('off', 'melody', 'full')):
        raise ValueError('invalid_planning_mode')
    supplied = body.get('sampling', {})
    if not isinstance(supplied, dict) or set(supplied) - set(spec['groups']):
        raise ValueError('invalid_sampling_group')
    if planning == 'off' and supplied.get('score'):
        raise ValueError('score_sampling_requires_active_planning')
    effective = {}
    for group, group_spec in spec['groups'].items():
        if group == 'score' and planning == 'off':
            continue
        fields = group_spec['properties']
        overrides = supplied.get(group, {})
        if not isinstance(overrides, dict) or set(overrides) - set(fields):
            raise ValueError('invalid_sampling_parameter:' + group)
        effective[group] = {}
        for key, field in fields.items():
            value = overrides.get(key, field['default'])
            if 'enum' in field:
                valid = isinstance(value, str) and value in field['enum']
            else:
                valid = not isinstance(value, bool) and isinstance(value, (int, float))
                valid = valid and (not isinstance(value, float) or math.isfinite(value))
                valid = valid and (field['type'] != 'INT' or int(value) == value)
                valid = valid and ('min' not in field or value >= field['min']) and ('max' not in field or value <= field['max'])
            if not valid:
                raise ValueError('sampling_outside_native_spec:' + group + '.' + key)
            effective[group][key] = value
    return {'sampling': effective, 'planning_mode': planning}


def apply(graph, body):
    for group, values in body.get('sampling', {}).items():
        node = GROUPS[group][0]
        graph[node]['inputs'].update(copy.deepcopy(values))
    planning = body.get('planning_mode')
    if '33:31' in graph and planning:
        graph['33:31']['inputs']['value'] = planning != 'off'
        if planning != 'off':
            graph['33:24']['inputs']['mode'] = planning
            graph['33:25']['inputs']['mode'] = planning
