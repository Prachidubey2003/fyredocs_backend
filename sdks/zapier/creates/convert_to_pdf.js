// "Convert to PDF" action.
//
// Takes a publicly-reachable file URL (Zapier's "File"
// input type, which gives us either a URL or pre-uploaded
// bytes) and submits it as a `convert-to-pdf` job. Returns
// the new job id + status so downstream Zap steps can
// chain on `{{jobId}}` for follow-up actions (download,
// notify a Slack channel, etc.).
//
// We deliberately do NOT block on completion here — Fyredocs
// jobs are async and typical word/excel/ppt conversions
// take 5–30 seconds. Blocking would burn a Zapier task
// minute and surface as a generic timeout if the conversion
// is slow. The hook trigger above is the natural pair: a
// Zap that POSTs here and another Zap that fires on
// `document.converted` for the resulting job.

const CREATE_JOB_URL = 'https://api.fyredocs.com/api/jobs';

const perform = async (z, bundle) => {
  const response = await z.request({
    url: CREATE_JOB_URL,
    method: 'POST',
    body: {
      tool: 'convert-to-pdf',
      sourceUrl: bundle.inputData.sourceUrl,
      sourceFilename: bundle.inputData.sourceFilename,
      outputFilename: bundle.inputData.outputFilename,
    },
  });
  const data = response.data?.data ?? response.data ?? {};
  return {
    jobId: data.id,
    status: data.status ?? 'queued',
    tool: data.toolType ?? 'convert-to-pdf',
    createdAt: data.createdAt,
  };
};

module.exports = {
  key: 'convertToPdf',
  noun: 'Conversion',
  display: {
    label: 'Convert to PDF',
    description:
      'Submit a file (Word, Excel, PowerPoint, image, HTML, …) for asynchronous conversion to PDF. Returns the new job ID.',
  },
  operation: {
    inputFields: [
      {
        key: 'sourceUrl',
        label: 'Source file',
        type: 'file',
        required: true,
        helpText: 'A publicly-reachable URL OR a file passed from an upstream Zap step.',
      },
      {
        key: 'sourceFilename',
        label: 'Source filename',
        type: 'string',
        required: false,
        helpText:
          'Optional — the original filename, used to pick the right converter (defaults to the URL\'s last path segment).',
      },
      {
        key: 'outputFilename',
        label: 'Output filename',
        type: 'string',
        required: false,
        helpText: 'Optional — defaults to the source filename with `.pdf` swapped in.',
      },
    ],
    perform,
    sample: {
      jobId: 'job_01HW...',
      status: 'queued',
      tool: 'convert-to-pdf',
      createdAt: '2026-05-18T00:01:00Z',
    },
    outputFields: [
      { key: 'jobId', label: 'Job ID' },
      { key: 'status', label: 'Status' },
      { key: 'tool', label: 'Tool' },
      { key: 'createdAt', label: 'Submitted at', type: 'datetime' },
    ],
  },
};
