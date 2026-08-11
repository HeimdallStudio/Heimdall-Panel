import { useMemo } from 'react';
import { Input, InputNumber, Switch, Tabs, Select, Space } from 'antd';
import { BranchesOutlined, CompassOutlined, IdcardOutlined, InfoCircleOutlined, NodeIndexOutlined, SafetyCertificateOutlined, SettingOutlined } from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import type { AllSetting } from '@/models/setting';
import { SettingListItem } from '@/components/ui';
import { RemarkTemplateField } from '@/components/form';
import { useMediaQuery } from '@/hooks/useMediaQuery';
import { catTabLabel } from './catTabLabel';
import { sanitizePath, normalizePath } from './uriPath';
import { SMART_IRAN_DIRECT_RULES_JSON, isSmartIranDirectRules } from './smartIranDirect';

type HeimdallSettingExtras = {
  remarkModel?: string;
};

function getRemarkModelSetting(allSetting: unknown): string {
  return ((allSetting as HeimdallSettingExtras).remarkModel || '').toString();
}

function withRemarkModelPatch(remarkModel: string) {
  return { remarkModel } as Partial<never> & HeimdallSettingExtras;
}


const REMARK_MODELS: Record<string, string> = { i: 'Inbound', e: 'Email', o: 'Other' };
const REMARK_SAMPLES: Record<string, string> = { i: 'Germany', e: 'john', o: 'Relay' };
const REMARK_SEPARATORS = [' ', '-', '_', '@', ':', '~', '|', ',', '.', '/'];

const OURENUS_SUB_TEMPLATE_DIR = '/usr/local/x-ui/sub_templates/ourenus';
const SANAEI_SUB_TEMPLATE_SENTINEL = '__heimdall_sanaei_default__';
const CUSTOM_SUB_TEMPLATE_DIR = '/usr/local/x-ui/sub_templates/custom';

type SubscriptionTemplatePreset = 'default' | 'sanaei' | 'custom';

function getSubscriptionTemplatePreset(themeDir?: string): SubscriptionTemplatePreset {
  const normalized = (themeDir || '').trim();
  if (!normalized || normalized === OURENUS_SUB_TEMPLATE_DIR) return 'default';
  if (normalized === SANAEI_SUB_TEMPLATE_SENTINEL) return 'sanaei';
  return 'custom';
}
interface SubscriptionGeneralTabProps {
  allSetting: AllSetting;
  updateSetting: (patch: Partial<AllSetting>) => void;
}

export default function SubscriptionGeneralTab({ allSetting, updateSetting }: SubscriptionGeneralTabProps) {
  const { t } = useTranslation();
  const { isMobile } = useMediaQuery();

  const smartIranDirectEnabled = isSmartIranDirectRules(allSetting.subJsonRules || '');
  const subscriptionTemplatePreset = useMemo(
    () => getSubscriptionTemplatePreset(allSetting.subThemeDir),
    [allSetting.subThemeDir],
  );

  function setSmartIranDirectEnabled(enabled: boolean) {
    updateSetting({ subJsonRules: enabled ? SMART_IRAN_DIRECT_RULES_JSON : '' });
  }

  function setSubscriptionTemplatePreset(preset: SubscriptionTemplatePreset) {
    if (preset === 'default') {
      updateSetting({ subThemeDir: '' });
      return;
    }
    if (preset === 'sanaei') {
      updateSetting({ subThemeDir: SANAEI_SUB_TEMPLATE_SENTINEL });
      return;
    }

    const current = (allSetting.subThemeDir || '').trim();
    updateSetting({
      subThemeDir:
        current && current !== OURENUS_SUB_TEMPLATE_DIR && current !== SANAEI_SUB_TEMPLATE_SENTINEL
          ? current
          : CUSTOM_SUB_TEMPLATE_DIR,
    });
  }


  const remarkModel = useMemo(() => {
    const rm = getRemarkModelSetting(allSetting);
    return rm.length > 1 ? rm.substring(1).split('') : [];
  }, [allSetting]);

  const remarkSeparator = useMemo(() => {
    const rm = getRemarkModelSetting(allSetting) || '-';
    return rm.length > 1 ? rm.charAt(0) : '-';
  }, [allSetting]);

  const remarkSample = useMemo(() => {
    const parts = remarkModel.map((k: string) => REMARK_SAMPLES[k]);
    return parts.length === 0 ? '' : parts.join(remarkSeparator);
  }, [remarkModel, remarkSeparator]);

  function setRemarkModel(parts: string[]) {
    updateSetting(withRemarkModelPatch(remarkSeparator + parts.join('')));
  }

  function setRemarkSeparator(sep: string) {
    const tail = (getRemarkModelSetting(allSetting) || '-').substring(1);
    updateSetting(withRemarkModelPatch(sep + tail));
  }

  // Preserve Heimdall remark-model helpers after upstream sync; the full UI wiring is validated later.
  void REMARK_MODELS;
  void REMARK_SEPARATORS;
  void remarkSample;
  void setRemarkModel;
  void setRemarkSeparator;
  return (
    <Tabs defaultActiveKey="1" items={[
      {
        key: '1',
        label: catTabLabel(<SettingOutlined />, t('pages.settings.panelSettings'), isMobile),
        children: (
          <>
            <SettingListItem paddings="small" title={t('pages.settings.subEnable')} description={t('pages.settings.subEnableDesc')}>
              <Switch checked={allSetting.subEnable} onChange={(v) => updateSetting({ subEnable: v })} />
            </SettingListItem>
            <SettingListItem paddings="small" title={t('pages.settings.subJsonEnableTitle')} description={t('pages.settings.subJsonEnable')}>
              <Switch checked={allSetting.subJsonEnable} onChange={(v) => updateSetting({ subJsonEnable: v })} />
            </SettingListItem>
              <SettingListItem
                paddings="small"
                title={t('pages.settings.subClientImportFormat')}
                description={t('pages.settings.subClientImportFormatDesc')}
              >
                <Select
                  value={allSetting.subClientImportFormat || 'normal'}
                  onChange={(v) => updateSetting({ subClientImportFormat: v as 'normal' | 'json' })}
                  style={{ width: '100%' }}
                  options={[
                    { value: 'normal', label: t('pages.settings.subClientImportFormatNormal') },
                    { value: 'json', label: t('pages.settings.subClientImportFormatJson') },
                  ]}
                />
              </SettingListItem>
            <SettingListItem
              paddings="small"
              title="Smart Iran Direct for JSON Subscription"
              description="Bypass all .ir domains, 1000 selected Iranian non-.ir domains, geoip:ir and private IPs in JSON subscriptions. Supported subdomains are included automatically."
            >
              <Switch
                checked={smartIranDirectEnabled}
                disabled={!allSetting.subJsonEnable}
                onChange={setSmartIranDirectEnabled}
              />
            </SettingListItem>
            <SettingListItem paddings="small" title={t('pages.settings.subClashEnableTitle')}>
              <Switch checked={allSetting.subClashEnable} onChange={(v) => updateSetting({ subClashEnable: v })} />
            </SettingListItem>
            <SettingListItem paddings="small" title={t('pages.settings.subListen')} description={t('pages.settings.subListenDesc')}>
              <Input value={allSetting.subListen} onChange={(e) => updateSetting({ subListen: e.target.value })} />
            </SettingListItem>
            <SettingListItem paddings="small" title={t('pages.settings.subDomain')} description={t('pages.settings.subDomainDesc')}>
              <Input value={allSetting.subDomain} onChange={(e) => updateSetting({ subDomain: e.target.value })} />
            </SettingListItem>
            <SettingListItem paddings="small" title={t('pages.settings.subPort')} description={t('pages.settings.subPortDesc')}>
              <InputNumber value={allSetting.subPort} min={1} max={65535} style={{ width: '100%' }}
                onChange={(v) => updateSetting({ subPort: Number(v) || 0 })} />
            </SettingListItem>
            <SettingListItem paddings="small" title={t('pages.settings.subPath')} description={t('pages.settings.subPathDesc')}>
              <Input
                value={allSetting.subPath}
                placeholder="/sub/"
                onChange={(e) => updateSetting({ subPath: sanitizePath(e.target.value) })}
                onBlur={() => updateSetting({ subPath: normalizePath(allSetting.subPath) })}
              />
            </SettingListItem>
            <SettingListItem paddings="small" title={t('pages.settings.subURI')} description={t('pages.settings.subURIDesc')}>
              <Input value={allSetting.subURI} placeholder="(http|https)://domain[:port]/path/"
                onChange={(e) => updateSetting({ subURI: e.target.value })} />
            </SettingListItem>
          </>
        ),
      },
      {
        key: '2',
        label: catTabLabel(<InfoCircleOutlined />, t('pages.settings.information'), isMobile),
        children: (
          <>
            <SettingListItem paddings="small" title={t('pages.settings.subEncrypt')} description={t('pages.settings.subEncryptDesc')}>
              <Switch checked={allSetting.subEncrypt} onChange={(v) => updateSetting({ subEncrypt: v })} />
            </SettingListItem>
            <SettingListItem
              paddings="small"
              title={t('pages.settings.remarkTemplate')}
              description={t('pages.settings.remarkTemplateDesc')}
            >
              <RemarkTemplateField
                value={allSetting.remarkTemplate}
                onChange={(v) => updateSetting({ remarkTemplate: v })}
                maxLength={256}
              />
            </SettingListItem>

            <SettingListItem paddings="small" title={t('pages.settings.subUpdates')} description={t('pages.settings.subUpdatesDesc')}>
              <InputNumber value={allSetting.subUpdates} min={0} max={525600} style={{ width: '100%' }}
                onChange={(v) => updateSetting({ subUpdates: Number(v) || 0 })} />
            </SettingListItem>
          </>
        ),
      },
      {
        key: '3',
        label: catTabLabel(<IdcardOutlined />, t('pages.settings.profile'), isMobile),
        children: (
          <>
            <SettingListItem paddings="small" title={t('pages.settings.subTitle')} description={t('pages.settings.subTitleDesc')}>
              <Input value={allSetting.subTitle} onChange={(e) => updateSetting({ subTitle: e.target.value })} />
            </SettingListItem>
            <SettingListItem paddings="small" title={t('pages.settings.subSupportUrl')} description={t('pages.settings.subSupportUrlDesc')}>
              <Input value={allSetting.subSupportUrl} placeholder="https://example.com"
                onChange={(e) => updateSetting({ subSupportUrl: e.target.value })} />
            </SettingListItem>
            <SettingListItem paddings="small" title={t('pages.settings.subProfileUrl')} description={t('pages.settings.subProfileUrlDesc')}>
              <Input value={allSetting.subProfileUrl} placeholder="https://example.com"
                onChange={(e) => updateSetting({ subProfileUrl: e.target.value })} />
            </SettingListItem>
            <SettingListItem paddings="small" title={t('pages.settings.subAnnounce')} description={t('pages.settings.subAnnounceDesc')}>
              <Input.TextArea value={allSetting.subAnnounce}
                onChange={(e) => updateSetting({ subAnnounce: e.target.value })} />
            </SettingListItem>
            <SettingListItem
              paddings="small"
              title={t('pages.settings.subThemeDir')}
              description={(
                <>
                  {t('pages.settings.subThemeDirDesc')}{' '}
                  <a
                    href="https://github.com/HeimdallStudio/Heimdall-Panel/blob/main/docs/custom-subscription-templates.md"
                    target="_blank"
                    rel="noopener noreferrer"
                  >
                    {t('pages.settings.subThemeDirDocs')}
                  </a>
                </>
              )}
            >
              <Space direction="vertical" style={{ width: '100%' }}>
                  <Select
                    value={subscriptionTemplatePreset}
                    onChange={setSubscriptionTemplatePreset}
                    style={{ width: '100%' }}
                    options={[
                      { value: 'default', label: 'Default Heimdall' },
                      { value: 'sanaei', label: 'Sanaei' },
                      { value: 'custom', label: 'Custom Path' },
                    ]}
                  />
                  {subscriptionTemplatePreset === 'custom' && (
                    <Input
                      value={allSetting.subThemeDir}
                      placeholder="/usr/local/x-ui/sub_templates/custom"
                      onChange={(e) => updateSetting({ subThemeDir: e.target.value })}
                    />
                  )}
                </Space>
            </SettingListItem>
          </>
        ),
      },
      {
        key: '4',
        label: catTabLabel(<SafetyCertificateOutlined />, t('pages.settings.certs'), isMobile),
        children: (
          <>
            <SettingListItem paddings="small" title={t('pages.settings.subCertPath')} description={t('pages.settings.subCertPathDesc')}>
              <Input value={allSetting.subCertFile} onChange={(e) => updateSetting({ subCertFile: e.target.value })} />
            </SettingListItem>
            <SettingListItem paddings="small" title={t('pages.settings.subKeyPath')} description={t('pages.settings.subKeyPathDesc')}>
              <Input value={allSetting.subKeyFile} onChange={(e) => updateSetting({ subKeyFile: e.target.value })} />
            </SettingListItem>
          </>
        ),
      },
      {
        key: '5',
        label: catTabLabel(<BranchesOutlined />, 'Happ', isMobile),
        children: (
          <>
            <SettingListItem paddings="small" title={t('pages.settings.subEnableRouting')} description={t('pages.settings.subEnableRoutingDesc')}>
              <Switch checked={allSetting.subEnableRouting} onChange={(v) => updateSetting({ subEnableRouting: v })} />
            </SettingListItem>
            <SettingListItem paddings="small" title={t('pages.settings.subRoutingRules')} description={t('pages.settings.subRoutingRulesDesc')}>
              <Input.TextArea value={allSetting.subRoutingRules} placeholder="happ://routing/add/..."
                onChange={(e) => updateSetting({ subRoutingRules: e.target.value })} />
            </SettingListItem>
            <SettingListItem paddings="small" title={t('pages.settings.subHideSettings')} description={t('pages.settings.subHideSettingsDesc')}>
              <Switch checked={allSetting.subHideSettings} onChange={(v) => updateSetting({ subHideSettings: v })} />
            </SettingListItem>
          </>
        ),
      },
      {
        key: '6',
        label: catTabLabel(<NodeIndexOutlined />, 'Clash / Mihomo', isMobile),
        children: (
          <>
            <SettingListItem paddings="small" title={t('pages.settings.subClashEnableRouting')} description={t('pages.settings.subClashEnableRoutingDesc')}>
              <Switch checked={allSetting.subClashEnableRouting} onChange={(v) => updateSetting({ subClashEnableRouting: v })} />
            </SettingListItem>
            <SettingListItem paddings="small" title={t('pages.settings.subClashRoutingRules')} description={t('pages.settings.subClashRoutingRulesDesc')}>
              <Input.TextArea
                value={allSetting.subClashRules}
                rows={8}
                placeholder={'GEOSITE,category-ir,DIRECT\nGEOIP,private,DIRECT'}
                onChange={(e) => updateSetting({ subClashRules: e.target.value })}
              />
            </SettingListItem>
          </>
        ),
      },
      {
        key: '7',
        label: catTabLabel(<CompassOutlined />, 'Incy', isMobile),
        children: (
          <>
            <SettingListItem paddings="small" title={t('pages.settings.subIncyEnableRouting')} description={t('pages.settings.subIncyEnableRoutingDesc')}>
              <Switch checked={allSetting.subIncyEnableRouting} onChange={(v) => updateSetting({ subIncyEnableRouting: v })} />
            </SettingListItem>
            <SettingListItem paddings="small" title={t('pages.settings.subIncyRoutingRules')} description={t('pages.settings.subIncyRoutingRulesDesc')}>
              <Input.TextArea value={allSetting.subIncyRoutingRules} placeholder="incy://routing/onadd/..."
                onChange={(e) => updateSetting({ subIncyRoutingRules: e.target.value })} />
            </SettingListItem>
          </>
        ),
      },
    ]} />
  );
}
