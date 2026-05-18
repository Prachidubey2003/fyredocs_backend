// "Sign PDF" action.
//
// Submits a sign-pdf job against an existing document. The
// caller provides the document id (typically pulled from a
// `document.converted` trigger upstream) and the signature
// metadata. Returns the new job id.
//
// We expose only the visible-stamp signature path here; the
// cryptographic PAdES variant is a separate roadmap item. The
// payload still threads a `signerId` so the resulting
// `document.signed` event carries the right attribution
// (matches the eventbridge sign-event enrichment).

const CREATE_JOB_URL = 'https://api.fyredocs.com/api/jobs';

const perform = async (z, bundle) => {
  const response = await z.request({
    url: CREATE_JOB_URL,
    method: 'POST',
    body: {
      tool: 'sign-pdf',
      documentId: bundle.inputData.documentId,
      page: bundle.inputData.page ?? null,
      position: bundle.inputData.position ?? 'br',
      signatureData: bundle.inputData.signatureData,
    },
  });
  const data = response.data?.data ?? response.data ?? {};
  return {
    jobId: data.id,
    status: data.status ?? 'queued',
    documentId: bundle.inputData.documentId,
    createdAt: data.createdAt,
  };
};

module.exports = {
  key: 'signPdf',
  noun: 'Signature',
  display: {
    label: 'Sign PDF',
    description:
      'Apply a visible signature stamp to an existing document. Returns the job ID; a `document.signed` event fires when the stamp lands.',
  },
  operation: {
    inputFields: [
      {
        key: 'documentId',
        label: 'Document ID',
        type: 'string',
        required: true,
        helpText:
          'The document to sign. Typically piped from a `document.converted` trigger or a prior `Convert to PDF` action.',
      },
      {
        key: 'signatureData',
        label: 'Signature image (data URL)',
        type: 'text',
        required: true,
        helpText:
          'A `data:image/png;base64,…` URL of the signature graphic. Most signature-capture upstream apps emit this shape.',
      },
      {
        key: 'page',
        label: 'Page number',
        type: 'integer',
        required: false,
        helpText: 'Optional — defaults to the last page of the document.',
      },
      {
        key: 'position',
        label: 'Position',
        type: 'string',
        required: false,
        default: 'br',
        choices: {
          tl: 'Top-left',
          tc: 'Top-center',
          tr: 'Top-right',
          c: 'Center',
          bl: 'Bottom-left',
          bc: 'Bottom-center',
          br: 'Bottom-right',
        },
        helpText: 'Where on the page the signature lands. Defaults to bottom-right.',
      },
    ],
    perform,
    sample: {
      jobId: 'job_01HW...',
      status: 'queued',
      documentId: 'doc_01HV...',
      createdAt: '2026-05-18T00:01:00Z',
    },
    outputFields: [
      { key: 'jobId', label: 'Job ID' },
      { key: 'status', label: 'Status' },
      { key: 'documentId', label: 'Document ID' },
      { key: 'createdAt', label: 'Submitted at', type: 'datetime' },
    ],
  },
};
