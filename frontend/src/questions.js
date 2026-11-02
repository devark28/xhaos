export const hasPendingQuestions = agent => agent.questionBatch?.status==='pending';
export function answerQuestions(agent, batchId, answers) {
  const batch=agent.questionBatch;
  if(!hasPendingQuestions(agent)||batch.id!==batchId||agent.status==='terminated')throw new Error('This question request is no longer pending.');
  const validated={};
  for(const question of batch.questions){
    const answer=answers[question.id]||{}, choices=[...new Set(answer.choices||[])], text=String(answer.text||'').trim();
    const optionIds=new Set((question.options||[]).map(option=>option.id));
    if(choices.some(id=>!optionIds.has(id))||(!question.multiple&&choices.length>1))throw new Error(`Choose a valid answer for “${question.prompt}”.`);
    if(question.required!==false&&!choices.length&&!text)throw new Error(`Answer “${question.prompt}” or write your own response.`);
    validated[question.id]={choices,text};
  }
  batch.answers=validated;batch.status='answered';batch.answeredAt=new Date().toISOString();delete batch.draft;
  const text=batch.questions.map(question=>{
    const answer=validated[question.id];
    const selected=(question.options||[]).filter(option=>answer.choices.includes(option.id)).map(option=>option.label).join(', ');
    return `${question.prompt}\n${[selected,answer.text].filter(Boolean).join('\n')||'(Skipped)'}`;
  }).join('\n\n');
  (agent.activity ||= []).push({kind:'user',text,questionBatchId:batchId});
  if(agent.status==='waiting')agent.status='running';
  return validated;
}
export function demoQuestions() {
  return {id:'demo-backend-questions-v1',status:'pending',title:'A few decisions before I continue',questions:[
    {id:'runner',title:'Backend route',prompt:'Which route should the first dogfooding adapter use?',options:[{id:'cli',label:'Existing CLI runner',description:'Use an installed agent runner for the first end-to-end path.'},{id:'api',label:'Direct model API',description:'Own the model conversation and tool loop inside Xhaos.'}]},
    {id:'scope',title:'Change scope',prompt:'What should the first agent be allowed to change?',options:[{id:'docs',label:'Documentation only'},{id:'feature',label:'One small frontend feature'}]},
    {id:'notes',title:'Constraints',prompt:'Any constraints I should know about?',required:false,options:[]}
  ]};
}
export function ensureQuestionDemo(workspaces, workspace) {
  const existing=workspace.agents.find(agent=>agent.demoScenario==='composer-question-v1');
  if(existing)return existing;
  const ids=[];const visit=agents=>agents.forEach(agent=>{ids.push(agent.id);visit(agent.children||[]);});
  workspaces.forEach(w=>visit(w.agents));
  const agent={id:Math.max(0,...ids)+1,title:'Clarify the first backend dogfooding run',role:'Planning',status:'waiting',model:'GPT-6.1-sol',time:'0m',progress:0,demo:true,demoScenario:'composer-question-v1',message:'I need a few decisions before I proceed.',activity:[{kind:'assistant',text:'This is a simulated question request. Please choose an option or answer in your own words for the batch shown in the composer.'}],questionBatch:demoQuestions(),children:[]};
  workspace.agents.push(agent);
  return agent;
}
