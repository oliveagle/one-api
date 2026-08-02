import React, { useCallback, useEffect, useRef, useState } from 'react';
import {
  Button,
  Card,
  Checkbox,
  Icon,
  Label,
  Message,
  Segment,
  Table,
} from 'semantic-ui-react';
import { useTranslation } from 'react-i18next';
import { API, showError, showSuccess, timestamp2string } from '../../helpers';

const REFRESH_INTERVAL_MS = 5000;

function renderCooldown(until) {
  if (!until) {
    return <Label basic color='green'>正常</Label>;
  }
  return <Label basic color='orange'>冷却中</Label>;
}

const Routing = () => {
  const { t } = useTranslation();
  const [data, setData] = useState(null);
  const [loading, setLoading] = useState(true);
  const [autoRefresh, setAutoRefresh] = useState(true);
  const timerRef = useRef(null);

  const load = useCallback(async () => {
    try {
      const res = await API.get('/api/routing/status');
      const { success, message, data } = res.data;
      if (success) {
        setData(data);
      } else {
        showError(message);
      }
    } catch (err) {
      showError(err.message);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    load();
    return () => {
      if (timerRef.current) {
        clearInterval(timerRef.current);
      }
    };
  }, [load]);

  useEffect(() => {
    if (timerRef.current) {
      clearInterval(timerRef.current);
      timerRef.current = null;
    }
    if (autoRefresh) {
      timerRef.current = setInterval(load, REFRESH_INTERVAL_MS);
    }
  }, [autoRefresh, load]);

  async function unbind(sessionKey) {
    try {
      const res = await API.delete('/api/routing/session', {
        data: { session_key: sessionKey },
      });
      const { success, message } = res.data;
      if (success) {
        showSuccess(t('routing.unbind_success'));
        await load();
      } else {
        showError(message);
      }
    } catch (err) {
      showError(err.message);
    }
  }

  async function clearAll() {
    try {
      const res = await API.delete('/api/routing/sessions');
      const { success, message } = res.data;
      if (success) {
        showSuccess(t('routing.clear_success'));
        await load();
      } else {
        showError(message);
      }
    } catch (err) {
      showError(err.message);
    }
  }

  const sessions = data ? data.sessions : [];
  const channels = data ? data.channels : [];
  const totalRequests = sessions.reduce((sum, s) => sum + s.requests, 0);

  return (
    <div className='dashboard-container'>
      <Card fluid className='chart-card'>
        <Card.Content>
          <Card.Header className='header'>
            {t('routing.title')}
            <div style={{ float: 'right' }}>
              <Checkbox
                toggle
                checked={autoRefresh}
                onChange={(e, { checked }) => setAutoRefresh(checked)}
                label={t('routing.auto_refresh')}
              />
              <Button
                style={{ marginLeft: '12px' }}
                size='small'
                onClick={() => load()}
                loading={loading}
              >
                <Icon name='refresh' />
                {t('routing.refresh')}
              </Button>
              <Button
                color='red'
                size='small'
                style={{ marginLeft: '8px' }}
                onClick={clearAll}
                disabled={sessions.length === 0}
              >
                <Icon name='trash' />
                {t('routing.clear_all')}
              </Button>
            </div>
          </Card.Header>

          {data && (
            <Segment basic>
              <Label
                color={data.enabled ? 'green' : 'grey'}
                size='large'
                basic
              >
                <Icon name={data.enabled ? 'check circle' : 'ban'} />
                {data.enabled ? t('routing.enabled') : t('routing.disabled')}
              </Label>
              <Label size='large' basic>
                <Icon name='cubes' />
                {t('routing.models')}: {data.models.join(', ')}
              </Label>
              <Label size='large' basic>
                <Icon name='hourglass half' />
                {t('routing.cooldown_seconds')}: {data.cooldown_seconds}
              </Label>
              <Label size='large' basic>
                <Icon name='clock outline' />
                {t('routing.session_ttl_seconds')}: {data.session_ttl_seconds}
              </Label>
              <Label size='large' basic>
                <Icon name='sliders horizontal' />
                {t('routing.session_id_header')}: {data.session_id_header}
              </Label>
            </Segment>
          )}

          {data && (
            <Message info>
              {t('routing.statistics')}:{' '}
              <b>{sessions.length}</b> {t('routing.active_sessions')},{' '}
              <b>{totalRequests}</b> {t('routing.total_requests')},{' '}
              <b>{channels.length}</b> {t('routing.active_channels')}
            </Message>
          )}

          <Table basic='very' compact>
            <Table.Header>
              <Table.Row>
                <Table.HeaderCell>{t('routing.channel_header')}</Table.HeaderCell>
                <Table.HeaderCell>{t('routing.active_sessions')}</Table.HeaderCell>
                <Table.HeaderCell>{t('routing.channel_state')}</Table.HeaderCell>
              </Table.Row>
            </Table.Header>
            <Table.Body>
              {channels.map((ch) => (
                <Table.Row key={ch.channel_id}>
                  <Table.Cell>#{ch.channel_id}</Table.Cell>
                  <Table.Cell>{ch.sessions}</Table.Cell>
                  <Table.Cell>{renderCooldown(ch.cooling_until)}</Table.Cell>
                </Table.Row>
              ))}
              {channels.length === 0 && (
                <Table.Row>
                  <Table.Cell colSpan='3' textAlign='center'>
                    {t('routing.no_channels')}
                  </Table.Cell>
                </Table.Row>
              )}
            </Table.Body>
          </Table>

          <Table basic='very' compact>
            <Table.Header>
              <Table.Row>
                <Table.HeaderCell>{t('routing.session_key')}</Table.HeaderCell>
                <Table.HeaderCell>{t('routing.model')}</Table.HeaderCell>
                <Table.HeaderCell>{t('routing.group')}</Table.HeaderCell>
                <Table.HeaderCell>{t('routing.channel_id')}</Table.HeaderCell>
                <Table.HeaderCell>{t('routing.requests')}</Table.HeaderCell>
                <Table.HeaderCell>{t('routing.failures')}</Table.HeaderCell>
                <Table.HeaderCell>{t('routing.last_seen')}</Table.HeaderCell>
                <Table.HeaderCell>{t('routing.action')}</Table.HeaderCell>
              </Table.Row>
            </Table.Header>
            <Table.Body>
              {sessions.map((s) => (
                <Table.Row key={`${s.session_key}-${s.model}-${s.channel_id}`}>
                  <Table.Cell>
                    <code>{s.session_key}</code>
                  </Table.Cell>
                  <Table.Cell>{s.model}</Table.Cell>
                  <Table.Cell>{s.group}</Table.Cell>
                  <Table.Cell>#{s.channel_id}</Table.Cell>
                  <Table.Cell>{s.requests}</Table.Cell>
                  <Table.Cell>
                    {s.failures > 0 ? (
                      <Label basic color='orange'>
                        {s.failures}
                      </Label>
                    ) : (
                      0
                    )}
                  </Table.Cell>
                  <Table.Cell>{timestamp2string(s.last_seen)}</Table.Cell>
                  <Table.Cell>
                    <Button
                      size='mini'
                      color='blue'
                      onClick={() => unbind(s.session_key)}
                    >
                      <Icon name='unlinkify' />
                      {t('routing.unbind')}
                    </Button>
                  </Table.Cell>
                </Table.Row>
              ))}
              {sessions.length === 0 && (
                <Table.Row>
                  <Table.Cell colSpan='8' textAlign='center'>
                    {t('routing.no_sessions')}
                  </Table.Cell>
                </Table.Row>
              )}
            </Table.Body>
          </Table>
        </Card.Content>
      </Card>
    </div>
  );
};

export default Routing;
