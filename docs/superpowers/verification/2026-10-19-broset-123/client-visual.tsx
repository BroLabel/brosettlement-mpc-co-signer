import React from 'react';
import {createRoot} from 'react-dom/client';
import './src/index.css';
import i18n from './src/lib/i18n';
await i18n.changeLanguage('en');
const revision='0123456789abcdef0123456789abcdef01234567';
const cases = [
 {label:'Known long development version', status:'ONLINE', version:'2.2.0-dev+0123456789ab.' + 'a'.repeat(100), revision},
 {label:'Unknown legacy metadata', status:'ONLINE', version:null, revision:null},
 {label:'Offline last reported release', status:'OFFLINE', version:'2.1.1', revision},
];

import {QueryClient, QueryClientProvider} from '@tanstack/react-query';
import {BrowserRouter} from 'react-router-dom';
import {MpcReadinessCard} from './src/pages/mpc/components/mpc-readiness-card';
import {SetupStepCosigner} from './src/pages/organization-setup/components/setup-step-cosigner';
import {useAuthStore} from './src/pages/auth/auth-store';
useAuthStore.setState({user:{scopes:[['*']]} as never});
createRoot(document.getElementById('root')!).render(
 <QueryClientProvider client={new QueryClient()}><BrowserRouter><main className="p-4 space-y-4">
 <h1 className="text-lg font-semibold">BROSET-123 client visual fixtures</h1>
 <div className="grid gap-6 lg:grid-cols-3">{cases.map(item => {
 const mpc={orgId:'fixture',keyId:null,keyStatus:null,mpcInitialization:{status:'NOT_INITIALIZED',keyId:null},chains:[{chain:'tron:nile',status:'MPC_KEY_NOT_READY'}],coSigner:{...item,lastSeenAt:'2026-10-19T10:00:00.000Z'}};
 const onboarding={orgId:undefined,isTestnetOnly:true,mpc,derived:{step1Complete:true,step2Complete:true,coSignerOnline:item.status==='ONLINE'},mpcQuery:{isFetching:false,refetch:async()=>{}}};
 return <section key={item.label} className="min-w-0 space-y-4"><h2>{item.label}</h2>
 <MpcReadinessCard data={mpc as never} isError={false} isLoading={false}/>
 <div className="rounded-lg border p-4"><h3 className="mb-4 font-semibold">Organization setup</h3><SetupStepCosigner onboarding={onboarding as never}/></div>
 </section>})}</div></main></BrowserRouter></QueryClientProvider>);
