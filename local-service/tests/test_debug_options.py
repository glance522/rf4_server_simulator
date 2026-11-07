from contextlib import closing
import json
from pathlib import Path
import sqlite3
import sys
import tempfile
import unittest
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import rf4_local_options as options
import test_portal as portal


class DebugSettingsTests(unittest.TestCase):
    def setUp(self):
        self.temp=tempfile.TemporaryDirectory(prefix='rf4-debug-')
        self.path=Path(self.temp.name)/'gameplay.sqlite3'
        with closing(sqlite3.connect(self.path)) as db:
            db.execute('CREATE TABLE tunables(key TEXT PRIMARY KEY,value TEXT,updated_by TEXT,updated_ts REAL)')
            db.commit()
    def tearDown(self): self.temp.cleanup()
    def rows(self):
        with closing(sqlite3.connect(self.path)) as db:
            return db.execute('SELECT * FROM tunables ORDER BY key').fetchall()
    def put(self,values):
        with closing(sqlite3.connect(self.path)) as db:
            db.executemany('INSERT OR REPLACE INTO tunables VALUES(?,?,?,?)',[(k,v,'fixture',1) for k,v in values.items()])
            db.commit()
    def test_new_database_defaults_off_without_any_write(self):
        before=self.path.read_bytes()
        for _ in range(2):
            state=options.debug_status(self.path)
            self.assertFalse(state['enabled']);self.assertFalse(state['configured'])
        self.assertEqual(self.rows(),[]);self.assertEqual(self.path.read_bytes(),before)
        self.assertFalse(hasattr(options,'ensure_debug_defaults'))
    def test_existing_enabled_custom_values_and_state_are_never_initialized_again(self):
        state={'schema':1,'enabled':True,'originals':{k:None for k in options.DEBUG_VALUES}}
        self.put({'debug_mode':'1','fish_weight_scale':'2.5','bite_delay_max':'11',options.DEBUG_STATE_KEY:json.dumps(state)})
        before=self.rows()
        for _ in range(2):
            value=options.debug_status(self.path)
            self.assertTrue(value['enabled']);self.assertFalse(value['presetActive'])
            self.assertEqual(value['weightMultiplier'],2.5)
        self.assertEqual(self.rows(),before)
    def test_explicit_enable_disable_restores_originals_and_missing_keys(self):
        self.put({'fish_weight_scale':'1.75','bite_delay_max':'9','untouched':'original'})
        self.assertTrue(options.set_debug(self.path,True)['presetActive'])
        self.assertFalse(options.set_debug(self.path,False)['enabled'])
        values={row[0]:row[1] for row in self.rows()}
        self.assertEqual(values['fish_weight_scale'],'1.75');self.assertEqual(values['bite_delay_max'],'9')
        self.assertNotIn('bite_delay_fixed',values);self.assertNotIn('bite_delay_scale',values)
        self.assertEqual(values['untouched'],'original')
        options.set_debug(self.path,True);options.set_debug(self.path,False)
        self.assertEqual({row[0]:row[1] for row in self.rows()},values)
    def test_preset_matching_requires_present_numeric_values(self):
        self.put({'debug_mode':'1',**options.DEBUG_VALUES,'bite_delay_scale':'1.0'})
        self.assertTrue(options.debug_status(self.path)['presetActive'])
        for key in options.DEBUG_VALUES:
            self.put(options.DEBUG_VALUES)
            with closing(sqlite3.connect(self.path)) as db:
                db.execute('DELETE FROM tunables WHERE key=?',(key,));db.commit()
            self.assertFalse(options.debug_status(self.path)['presetActive'])
    def test_invalid_state_or_status_never_overwrites_existing_values(self):
        self.put({options.DEBUG_STATE_KEY:'invalid','debug_mode':'1','fish_weight_scale':'2'})
        before=self.rows()
        with self.assertRaises(options.LocalOptionsError): options.debug_status(self.path)
        with self.assertRaises(options.LocalOptionsError): options.set_debug(self.path,False)
        self.assertEqual(self.rows(),before)
        with self.assertRaises(options.LocalOptionsError): options.set_debug(self.path,'false')
        self.assertEqual(self.rows(),before)


class DebugHTTPTests(unittest.TestCase):
    setUp=portal.PortalTests.setUp
    tearDown=portal.PortalTests.tearDown
    request=portal.PortalTests.request
    def test_debug_requires_session_and_read_does_not_write(self):
        with closing(sqlite3.connect(self.root/'gameplay.sqlite3')) as db:
            db.execute('CREATE TABLE tunables(key TEXT PRIMARY KEY,value TEXT,updated_by TEXT,updated_ts REAL)');db.commit()
        before=(self.root/'gameplay.sqlite3').read_bytes()
        self.assertEqual(self.request('GET','/api/debug','invalid')[0],401)
        self.assertEqual(self.request('GET','/api/debug?uid=43')[0],400)
        self.assertEqual(self.request('GET','/api/debug',Origin='https://example.invalid')[0],403)
        status,raw=self.request('GET','/api/debug')
        self.assertEqual(status,200);self.assertFalse(json.loads(raw)['enabled'])
        self.assertEqual((self.root/'gameplay.sqlite3').read_bytes(),before)

    def test_only_explicit_authenticated_put_can_change_the_preset(self):
        with closing(sqlite3.connect(self.root/'gameplay.sqlite3')) as db:
            db.execute('CREATE TABLE tunables(key TEXT PRIMARY KEY,value TEXT,updated_by TEXT,updated_ts REAL)');db.commit()
        self.assertEqual(self.request('PUT','/api/debug','invalid',data={'enabled':True})[0],401)
        self.assertEqual(self.request('PUT','/api/debug',data={'enabled':True,'uid':43})[0],400)
        status,raw=self.request('PUT','/api/debug',data={'enabled':True})
        self.assertEqual(status,200);self.assertTrue(json.loads(raw)['presetActive'])
        status,raw=self.request('PUT','/api/debug',data={'enabled':False})
        self.assertEqual(status,200);self.assertFalse(json.loads(raw)['enabled'])
