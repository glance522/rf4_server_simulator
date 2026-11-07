from contextlib import closing
import json
from pathlib import Path
import sqlite3
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch
from uuid import uuid4
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import rf4_local_membership as member
from rf4_local_options import LocalOptionsError
import test_portal as portal


def state():
    return {'ok':True,'membership':{'active':False,'permanent':False,'expiresAt':0,'xpBonusRate':0},
            'balance':{'gold':2000,'silver':100},'plans':[{'id':'premium_1d','name':'高级会员 1 天','days':1,'priceGold':100}]}


class MembershipAdapterTests(unittest.TestCase):
    def setUp(self):
        self.temp=tempfile.TemporaryDirectory(prefix='rf4-member-')
        self.root=Path(self.temp.name);self.database=self.root/'gameplay.sqlite3';self.database.touch()
    def tearDown(self):self.temp.cleanup()
    def response(self,value,code=0):return subprocess.CompletedProcess([],code,json.dumps(value))
    def test_status_and_purchase_use_existing_cli_without_shell_and_reuse_request_id(self):
        request=str(uuid4());value=state();value['purchase']={'requestID':request,'planID':'premium_1d','chargedGold':100,'expiresAt':2000000000,'replayed':False}
        with patch.object(member.subprocess,'run',return_value=self.response(value)) as run:
            member.membership(self.database,self.root,42)
            self.assertEqual(run.call_args.args[0][-1],'status')
            for _ in range(2):member.membership(self.database,self.root,42,plan_id='premium_1d',request_id=request)
            command=run.call_args.args[0]
            self.assertEqual(command[-5:],['purchase','--plan','premium_1d','--request-id',request])
            self.assertEqual(command[command.index('--uid')+1],'42')
            self.assertNotIn('shell',run.call_args.kwargs)
            self.assertEqual(run.call_args.kwargs['stdin'],subprocess.DEVNULL)
    def test_rejects_invalid_uid_plan_request_without_starting_a_process(self):
        with patch.object(member.subprocess,'run') as run:
            for uid,plan,request in [(True,None,None),(0,None,None),(42,'other',str(uuid4())),(42,'premium_1d','invalid')]:
                with self.assertRaises(LocalOptionsError): member.membership(self.database,self.root,uid,plan_id=plan,request_id=request)
            run.assert_not_called()
    def test_uncertain_response_is_retryable_and_business_denial_is_explicit(self):
        request=str(uuid4())
        for result in (subprocess.TimeoutExpired('fixture',1),self.response({'ok':True}),self.response(state())):
            kwargs={'side_effect':result} if isinstance(result,Exception) else {'return_value':result}
            with patch.object(member.subprocess,'run',**kwargs),self.assertRaises(LocalOptionsError) as caught:
                member.membership(self.database,self.root,42,plan_id='premium_1d',request_id=request)
            self.assertEqual(caught.exception.status,503)
        with patch.object(member.subprocess,'run',return_value=self.response({'ok':False,'error':{'code':'insufficient_gold'}},1)),self.assertRaises(LocalOptionsError) as caught:
            member.membership(self.database,self.root,42,plan_id='premium_1d',request_id=request)
        self.assertEqual(caught.exception.status,409)
    def test_response_for_a_different_order_is_never_accepted(self):
        value=state();value['purchase']={'requestID':str(uuid4()),'planID':'premium_1d','chargedGold':100,'expiresAt':2000000000,'replayed':False}
        with patch.object(member.subprocess,'run',return_value=self.response(value)),self.assertRaises(LocalOptionsError) as caught:
            member.membership(self.database,self.root,42,plan_id='premium_1d',request_id=str(uuid4()))
        self.assertEqual(caught.exception.status,503)


class MembershipHTTPTests(unittest.TestCase):
    setUp=portal.PortalTests.setUp
    tearDown=portal.PortalTests.tearDown
    request=portal.PortalTests.request
    def test_only_authenticated_account_identity_reaches_the_membership_cli(self):
        with patch('rf4_portal.membership',return_value=state()) as call:
            self.assertEqual(self.request('GET','/api/membership','invalid')[0],401)
            self.assertEqual(self.request('GET','/api/membership?uid=43')[0],400)
            self.assertEqual(self.request('POST','/api/membership/purchase',data={'planID':'premium_1d','requestID':str(uuid4()),'uid':43})[0],400)
            call.assert_not_called()
            self.assertEqual(self.request('GET','/api/membership','player_two')[0],200)
            self.assertEqual(call.call_args.args[2],43)
            request=str(uuid4())
            self.assertEqual(self.request('POST','/api/membership/purchase',data={'planID':'premium_1d','requestID':request})[0],200)
            self.assertEqual(call.call_args.args[2],42)
            self.assertEqual(call.call_args.kwargs,{'plan_id':'premium_1d','request_id':request})
