package worker

import (
	"github.com/sinksmell/bee-crontab/models"
	"github.com/sinksmell/bee-crontab/models/common"
	"math/rand"
	"os/exec"
	"strconv"
	"time"
)

// Executor 用于执行shell命令的执行器
type Executor struct {
}

var (
	// BeeCronExecutor 执行器的单例
	BeeCronExecutor *Executor
)

// InitExecutor 初始化执行器单例
func InitExecutor() (err error) {
	BeeCronExecutor = &Executor{}
	return
}

// ExecuteJob 执行传入的任务
func (executor *Executor) ExecuteJob(info *models.JobExecInfo) {
	var (
		cmd    *exec.Cmd
		output []byte
		err    error
		result *models.JobExecResult
		lock   *JobLock
	)

	// 初始化任务结果
	result = &models.JobExecResult{
		ExecInfo: info,
		Output:   make([]byte, 0),
	}

	// 启动协程来处理任务
	go func() {
		var (
			timer     *time.Timer   // 任务执行定时器
			sigchan   chan struct{} // 任务执行结束消息管道
			timeLimit time.Duration
		)
		timeOut, _ := strconv.Atoi(info.Job.TimeOut)
		timeLimit = time.Duration(timeOut) * 1000 * time.Millisecond
		sigchan = make(chan struct{}, 1)

		// 获取分布式锁
		// 防止任务被并发地调度
		lock = WorkerJobManager.NewLock(info.Job.Name)
		// 记录开始开始抢锁的时间
		result.StartTime = time.Now()

		// 牺牲一点调度的准确性
		// 防止某台机器时间不准导致的资源独占
		// 再锁定资源前 sleep 随机睡眠一小段时间
		// 这里设置的是0-500ms
		time.Sleep(time.Duration(rand.Intn(500)) * time.Millisecond)

		// 锁定资源 defer 延迟释放锁
		err = lock.TryLock()
		defer lock.UnLock()

		if err != nil {
			// 上锁失败
			result.Err = err
			result.EndTime = time.Now()
		} else {
			// 重置开始时间
			result.StartTime = time.Now()
			// 初始化shell命令
			cmd = exec.CommandContext(info.CancelCtx, "/bin/bash", "-c", info.Job.Command)
			timer = time.NewTimer(timeLimit)
			// 执行并捕获输出
			// 启动协程执行任务 外部定时
			go func() {
				timer.Reset(timeLimit)
				output, err = cmd.CombinedOutput()
				result.EndTime = time.Now()
				result.Output = output
				result.Err = err
				sigchan <- struct{}{}
			}()
			// 等待消息到达，如果超时那么强制杀死任务
			for {
				select {
				case <-timer.C:
					// 定时器到期 任务执行超时
					info.CancelFunc()
					result.Type = common.RES_TIMEOUT
					result.Output = []byte("timeout!")
					goto END
				case <-sigchan:
					// 在限制时间内执行完成
					result.Type = common.RES_SUCCESS
					goto END
				}
			}

		}
	END:
		// 任务执行结束 把结果返回给 scheduler
		// 从执行表中删除对应的记录
		BeeScheduler.PushJobResult(result)
	}()
}
