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

import {OrganizationCoSignerStatusBadge, OrganizationCoSignerStatusSection} from './src/pages/organizations/components/organization-co-signer-status';
createRoot(document.getElementById('root')!).render(<main className="p-4 space-y-4">
<h1 className="text-lg font-semibold">BROSET-123 admin visual fixtures</h1>
<div className="grid gap-6 lg:grid-cols-3">{cases.map(item => <section key={item.label} className="min-w-0 space-y-4">
<h2>{item.label}</h2><OrganizationCoSignerStatusSection coSigner={{...item,lastSeenAt:'2026-10-19T10:00:00.000Z'} as never}/>
<div className="rounded-lg border p-4"><p className="mb-3">Organization list status tooltip</p><OrganizationCoSignerStatusBadge coSigner={{...item,lastSeenAt:'2026-10-19T10:00:00.000Z'} as never}/></div>
</section>)}</div></main>);
